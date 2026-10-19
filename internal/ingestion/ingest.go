package ingestion

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"sort"

	"github.com/fin/multi-agent-rag/internal/registry"
	"github.com/fin/multi-agent-rag/pkg/embedder"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrIngestionInProgress is returned when another ingestion holds the repo's
// advisory lock.
var ErrIngestionInProgress = errors.New("ingestion: already in progress for repo")

// ErrEmptyIngest is returned when chunking+embedding yields zero chunks for a
// repo that already has a good active version. Refusing the swap in that case
// avoids flipping active_version to an empty version and deleting the last
// good copy.
var ErrEmptyIngest = errors.New("ingestion: no content to ingest")

// IngestResult summarizes a completed ingestion.
type IngestResult struct {
	Version           int
	ChunksWritten     int
	OldVersionDeleted int64
}

// Ingestor orchestrates chunk → embed → write → swap for one repo.
type Ingestor struct {
	pool   *pgxpool.Pool
	reg    *registry.Service
	w      *VectorWriter
	emb    embedder.Embedder
	chunk  ChunkOptions
	cstore *ContextStore // optional; nil = skip the context card
}

// NewIngestor wires the pipeline dependencies.
func NewIngestor(pool *pgxpool.Pool, reg *registry.Service, w *VectorWriter, emb embedder.Embedder) *Ingestor {
	return &Ingestor{pool: pool, reg: reg, w: w, emb: emb, chunk: DefaultChunkOptions()}
}

// WithContextStore enables capturing a per-repo context card on each ingest.
func (ing *Ingestor) WithContextStore(cs *ContextStore) *Ingestor {
	ing.cstore = cs
	return ing
}

// advisoryKey derives a stable int64 lock key from a repo id.
func advisoryKey(repoID string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(repoID))
	return int64(h.Sum64())
}

// Ingest performs a full atomic-swap ingestion for repoID over the given
// files (path → content). commitsBehind records how far behind HEAD the
// snapshot is (0 = current).
func (ing *Ingestor) Ingest(ctx context.Context, repoID string, files map[string]string, commitsBehind int) (IngestResult, error) {
	// Serialize same-repo ingestion via a session-scoped advisory lock held on
	// a dedicated connection for the duration. The lock is keyed on repoID
	// alone, so it can (and must) be acquired before the repo is loaded below.
	conn, err := ing.pool.Acquire(ctx)
	if err != nil {
		return IngestResult{}, fmt.Errorf("ingest: acquire conn: %w", err)
	}
	defer conn.Release()

	key := advisoryKey(repoID)
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&locked); err != nil {
		return IngestResult{}, fmt.Errorf("ingest: advisory lock: %w", err)
	}
	if !locked {
		return IngestResult{}, ErrIngestionInProgress
	}
	defer func() { _, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, key) }()

	// ActiveVersion MUST be read under the advisory lock: reading it before
	// acquiring the lock would let a concurrent ingestion for the same repo
	// complete (including MarkIngested, which advances active_version) in the
	// window between this read and the lock acquisition, causing this call to
	// compute a newVersion that collides with the version the other call just
	// wrote (TOCTOU version-collision).
	repo, err := ing.reg.Get(ctx, repoID)
	if err != nil {
		return IngestResult{}, fmt.Errorf("ingest: load repo %s: %w", repoID, err)
	}
	if ing.emb.Dimension() != repo.EmbeddingDim {
		return IngestResult{}, fmt.Errorf("ingest %s: %w: embedder %d, repo %d",
			repoID, embedder.ErrDimensionMismatch, ing.emb.Dimension(), repo.EmbeddingDim)
	}

	oldVersion := repo.ActiveVersion
	newVersion := oldVersion + 1

	embedded, err := ing.chunkAndEmbed(ctx, files)
	if err != nil {
		return IngestResult{}, err
	}
	if len(embedded) == 0 && oldVersion > 0 {
		// Refuse to swap in an empty version over a good active one: this
		// snapshot may be empty/binary-only or point at the wrong directory,
		// and proceeding would flip active_version to a version with no rows
		// and then delete the last good copy (oldVersion), causing silent
		// data loss. A legitimately empty FIRST ingest (oldVersion == 0) is
		// harmless and is allowed to proceed below.
		return IngestResult{}, fmt.Errorf("ingest %s: %w", repoID, ErrEmptyIngest)
	}

	// Clear any rows a prior crashed attempt left at the target version: if
	// WriteChunks committed but EnsureHNSWIndex or MarkIngested failed after
	// it, a retry recomputes the same newVersion and would otherwise insert a
	// duplicate full copy alongside the orphaned rows (code_embeddings has no
	// unique constraint / ON CONFLICT). Writers are serialized by the
	// advisory lock above, so this is safe to do unconditionally; it deletes
	// 0 rows in the common case.
	if _, err := ing.w.DeleteVersion(ctx, repoID, newVersion); err != nil {
		return IngestResult{}, fmt.Errorf("ingest %s: clear target version: %w", repoID, err)
	}

	written, err := ing.w.WriteChunks(ctx, repoID, newVersion, embedded)
	if err != nil {
		return IngestResult{}, fmt.Errorf("ingest %s: write: %w", repoID, err)
	}
	if err := ing.w.EnsureHNSWIndex(ctx, repoID); err != nil {
		return IngestResult{}, fmt.Errorf("ingest %s: index: %w", repoID, err)
	}

	// Capture the repo's context card at this version (raw docs; no LLM).
	// Written BEFORE MarkIngested so that once the active-version swap lands,
	// ContextStore.LoadActive (which joins on active_version) sees the new card
	// atomically with the new vectors.
	if ing.cstore != nil {
		if card := BuildContextCard(files); card != "" {
			if err := ing.cstore.Upsert(ctx, repoID, newVersion, card); err != nil {
				return IngestResult{}, fmt.Errorf("ingest %s: context card: %w", repoID, err)
			}
		}
	}

	if err := ing.reg.MarkIngested(ctx, repoID, commitsBehind, newVersion); err != nil {
		return IngestResult{}, fmt.Errorf("ingest %s: mark ingested: %w", repoID, err)
	}

	var deleted int64
	if oldVersion > 0 {
		deleted, err = ing.w.DeleteVersion(ctx, repoID, oldVersion)
		if err != nil {
			return IngestResult{}, fmt.Errorf("ingest %s: delete old version: %w", repoID, err)
		}
		if ing.cstore != nil {
			// Best-effort: the old card is superseded; a leftover row is
			// harmless (LoadActive joins on active_version) but we clean up.
			_ = ing.cstore.DeleteVersion(ctx, repoID, oldVersion)
		}
	}

	return IngestResult{Version: newVersion, ChunksWritten: written, OldVersionDeleted: deleted}, nil
}

// chunkAndEmbed chunks all files (in deterministic path order) and embeds every
// chunk, returning them paired with their vectors.
func (ing *Ingestor) chunkAndEmbed(ctx context.Context, files map[string]string) ([]EmbeddedChunk, error) {
	paths := make([]string, 0, len(files))
	for p := range files {
		if ExcludeFromIndex(p) {
			continue // dependency-lock/minified noise or a local secrets file — never indexed
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var chunks []Chunk
	for _, p := range paths {
		chunks = append(chunks, ChunkFile(p, files[p], ing.chunk)...)
	}
	if len(chunks) == 0 {
		return nil, nil
	}

	// Embed per chunk and skip any the embedder rejects (e.g. a pathological
	// chunk that exceeds the model's token limit → a 500), rather than aborting
	// the whole repo ingest. A context cancellation still aborts.
	out := make([]EmbeddedChunk, 0, len(chunks))
	var skipped int
	for _, c := range chunks {
		vecs, err := ing.emb.Embed(ctx, []string{c.Content})
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("embed: %w", ctx.Err())
			}
			skipped++
			continue
		}
		if len(vecs) != 1 {
			skipped++
			continue
		}
		out = append(out, EmbeddedChunk{Chunk: c, Embedding: vecs[0]})
	}
	if skipped > 0 {
		log.Printf("ingest: skipped %d/%d chunk(s) the embedder rejected", skipped, len(chunks))
	}
	return out, nil
}
