package ingestion

import (
	"context"
	"fmt"

	"github.com/fin/multi-agent-rag/internal/models"
	"github.com/fin/multi-agent-rag/internal/pgvec"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EmbeddedChunk pairs a chunk with its embedding vector.
type EmbeddedChunk struct {
	Chunk     Chunk
	Embedding []float32
}

// VectorWriter writes embeddings into the code_embeddings table and manages
// per-partition HNSW indexes.
type VectorWriter struct {
	pool *pgxpool.Pool
}

// NewVectorWriter returns a writer over the given pool.
func NewVectorWriter(pool *pgxpool.Pool) *VectorWriter {
	return &VectorWriter{pool: pool}
}

// WriteChunks inserts all chunks at the given version in one transaction and
// returns the number of rows written.
func (w *VectorWriter) WriteChunks(ctx context.Context, repoID string, version int, chunks []EmbeddedChunk) (int, error) {
	if len(chunks) == 0 {
		return 0, nil
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	batch := &pgx.Batch{}
	for _, ec := range chunks {
		kind := ec.Chunk.Kind
		if kind == "" {
			kind = KindCode // default for callers that don't classify
		}
		batch.Queue(
			`INSERT INTO code_embeddings
			 (repo_id, file_path, chunk_index, content, embedding, version, kind)
			 VALUES ($1,$2,$3,$4,$5::vector,$6,$7)`,
			repoID, ec.Chunk.FilePath, ec.Chunk.Index, ec.Chunk.Content,
			pgvec.Literal(ec.Embedding), version, string(kind))
	}
	br := tx.SendBatch(ctx, batch)
	for range chunks {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return 0, fmt.Errorf("insert chunk: %w", err)
		}
	}
	if err := br.Close(); err != nil {
		return 0, fmt.Errorf("close batch: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return len(chunks), nil
}

// EnsureHNSWIndex creates the cosine HNSW index on the repo's partition if it
// does not already exist.
func (w *VectorWriter) EnsureHNSWIndex(ctx context.Context, repoID string) error {
	part := models.Repo{ID: repoID}.PartitionName()
	idx := part + "_hnsw"
	sql := fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS %s ON %s USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 128)`,
		idx, part)
	if _, err := w.pool.Exec(ctx, sql); err != nil {
		return fmt.Errorf("create hnsw index on %s: %w", part, err)
	}
	return nil
}

// DeleteVersion removes all rows of the given version for the repo.
func (w *VectorWriter) DeleteVersion(ctx context.Context, repoID string, version int) (int64, error) {
	ct, err := w.pool.Exec(ctx,
		`DELETE FROM code_embeddings WHERE repo_id=$1 AND version=$2`, repoID, version)
	if err != nil {
		return 0, fmt.Errorf("delete version %d: %w", version, err)
	}
	return ct.RowsAffected(), nil
}
