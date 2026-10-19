package retrieval

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fin/multi-agent-rag/internal/ingestion"
	"github.com/fin/multi-agent-rag/internal/models"
	"github.com/fin/multi-agent-rag/internal/registry"
	"github.com/fin/multi-agent-rag/pkg/embedder"
	"github.com/fin/multi-agent-rag/pkg/reranker"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultPerRepoBudget = 20
	defaultFinalTopK     = 10
)

// Gateway is the retrieval query engine over per-repo pgvector partitions.
type Gateway struct {
	search *searcher
	reg    *registry.Service
	emb    embedder.Embedder
	rr     reranker.Reranker
}

// NewGateway wires the gateway dependencies.
func NewGateway(pool *pgxpool.Pool, reg *registry.Service, emb embedder.Embedder, rr reranker.Reranker) *Gateway {
	return &Gateway{search: newSearcher(pool), reg: reg, emb: emb, rr: rr}
}

// SearchRequest is a cross-repo retrieval query.
type SearchRequest struct {
	Query         string
	RepoIDs       []string
	PerRepoBudget int
	FinalTopK     int
}

// SearchResult is one reranked chunk.
type SearchResult struct {
	RepoID     string
	FilePath   string
	ChunkIndex int
	Content    string
	Distance   float64
	Score      float64
}

// RepoStaleness reports a queried repo's ingestion freshness.
type RepoStaleness struct {
	RepoID         string
	LastIngestedAt *time.Time
	CommitsBehind  int
	Stale          bool
}

// SearchResponse carries reranked results plus per-repo staleness.
type SearchResponse struct {
	Results   []SearchResult
	Staleness []RepoStaleness
}

// Search runs the retrieval chain: discover → embed → fan-out → rerank →
// truncate, returning results and per-repo staleness.
func (g *Gateway) Search(ctx context.Context, req SearchRequest) (SearchResponse, error) {
	budget := req.PerRepoBudget
	if budget <= 0 {
		budget = defaultPerRepoBudget
	}
	topK := req.FinalTopK
	if topK <= 0 {
		topK = defaultFinalTopK
	}

	repos, err := g.reg.Discover(ctx, req.RepoIDs)
	if err != nil {
		return SearchResponse{}, fmt.Errorf("discover: %w", err)
	}

	staleness := make([]RepoStaleness, 0, len(repos))
	for _, r := range repos {
		staleness = append(staleness, RepoStaleness{
			RepoID:         r.ID,
			LastIngestedAt: r.LastIngestedAt,
			CommitsBehind:  r.CommitsBehind,
			Stale:          r.IsStale(),
		})
		if g.emb.Dimension() != r.EmbeddingDim {
			return SearchResponse{}, fmt.Errorf("search %s: %w: embedder %d, repo %d",
				r.ID, embedder.ErrDimensionMismatch, g.emb.Dimension(), r.EmbeddingDim)
		}
	}

	// Only repos with a live active version have vectors to search.
	var live []models.Repo
	for _, r := range repos {
		if r.ActiveVersion > 0 {
			live = append(live, r)
		}
	}
	if len(live) == 0 {
		return SearchResponse{Results: nil, Staleness: staleness}, nil
	}

	qvecs, err := g.emb.Embed(ctx, []string{req.Query})
	if err != nil {
		return SearchResponse{}, fmt.Errorf("embed query: %w", err)
	}
	if len(qvecs) != 1 {
		return SearchResponse{}, fmt.Errorf("embed query: got %d vectors, want 1", len(qvecs))
	}
	qvec := qvecs[0]

	// Fan out one query per live repo concurrently.
	perRepo := make([][]Candidate, len(live))
	errs := make([]error, len(live))
	var wg sync.WaitGroup
	for i, r := range live {
		wg.Add(1)
		go func(i int, r models.Repo) {
			defer wg.Done()
			perRepo[i], errs[i] = g.search.topK(ctx, r.ID, r.ActiveVersion, budget, qvec)
		}(i, r)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return SearchResponse{}, fmt.Errorf("fan-out: %w", e)
		}
	}

	var candidates []Candidate
	for _, cs := range perRepo {
		candidates = append(candidates, cs...)
	}
	if len(candidates) == 0 {
		return SearchResponse{Results: nil, Staleness: staleness}, nil
	}

	var results []SearchResult
	switch {
	case isPassthrough(g.rr):
		// No real reranker (local/fake mode): the fake's lexical score is weak
		// (0/1 for code) and would flatten the ranking. Rank by the semantic
		// vector distance instead, exposing score = 1 - distance so scores are
		// meaningful similarities (higher = closer), not lexical counts.
		results = rankByDistance(candidates)
	default:
		docs := make([]string, len(candidates))
		for i, c := range candidates {
			docs[i] = c.Content
		}
		scores, err := g.rr.Rerank(ctx, req.Query, docs)
		switch {
		case err != nil:
			// The reranker is a quality enhancement, not a correctness
			// requirement. A down/slow/erroring reranker must not fail retrieval
			// (which backs the developer tool-loop AND the MCP knowledge base) —
			// degrade to distance ranking and let operators see it in the logs.
			slog.Warn("reranker unavailable; falling back to vector-distance ranking",
				"model", g.rr.Model(), "candidates", len(candidates), "err", err)
			results = rankByDistance(candidates)
		case len(scores) != len(candidates):
			slog.Warn("reranker returned wrong score count; falling back to vector-distance ranking",
				"model", g.rr.Model(), "got", len(scores), "want", len(candidates))
			results = rankByDistance(candidates)
		default:
			results = make([]SearchResult, len(candidates))
			for i, c := range candidates {
				results[i] = SearchResult{
					RepoID: c.RepoID, FilePath: c.FilePath, ChunkIndex: c.ChunkIndex,
					Content: c.Content, Distance: c.Distance, Score: scores[i],
				}
			}
		}
	}

	// Sort by score desc; break ties by ascending vector distance (the semantic
	// signal — so equal-score items are ordered most-similar-first), then by
	// stable deterministic keys.
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		if results[i].Distance != results[j].Distance {
			return results[i].Distance < results[j].Distance
		}
		if results[i].RepoID != results[j].RepoID {
			return results[i].RepoID < results[j].RepoID
		}
		if results[i].FilePath != results[j].FilePath {
			return results[i].FilePath < results[j].FilePath
		}
		return results[i].ChunkIndex < results[j].ChunkIndex
	})

	if len(results) > topK {
		results = results[:topK]
	}
	return SearchResponse{Results: results, Staleness: staleness}, nil
}

// rankByDistance scores candidates by their semantic vector distance
// (score = 1 - distance, higher = closer), the fallback ranking used when no
// real reranker is present (fake/passthrough) or when a real reranker is
// unavailable. The final sort (score desc, distance asc) orders the results.
func rankByDistance(candidates []Candidate) []SearchResult {
	results := make([]SearchResult, len(candidates))
	for i, c := range candidates {
		results[i] = SearchResult{
			RepoID: c.RepoID, FilePath: c.FilePath, ChunkIndex: c.ChunkIndex,
			Content: c.Content, Distance: c.Distance, Score: 1 - c.Distance,
		}
	}
	return results
}

// isPassthrough reports whether the reranker provides no genuine semantic
// signal (the fake), so retrieval should rank by vector distance instead.
func isPassthrough(r reranker.Reranker) bool {
	p, ok := r.(interface{ Passthrough() bool })
	return ok && p.Passthrough()
}

// GetFile reassembles a whole indexed file's content from its chunks (in
// chunk_index order at the repo's active version), de-duplicating the line
// overlap the chunker adds between adjacent windows so the result is the
// original file, not a chunk dump. found=false if the file isn't indexed.
func (g *Gateway) GetFile(ctx context.Context, repoID, filePath string) (content string, found bool, err error) {
	repo, err := g.reg.Get(ctx, repoID)
	if err != nil {
		return "", false, fmt.Errorf("get file: repo %q: %w", repoID, err)
	}
	chunks, err := g.search.fileChunks(ctx, repoID, filePath, repo.ActiveVersion)
	if err != nil {
		return "", false, fmt.Errorf("get file: %w", err)
	}
	if len(chunks) == 0 {
		return "", false, nil
	}
	return reassembleChunks(chunks, ingestion.DefaultChunkOptions().OverlapLines), true, nil
}

// reassembleChunks joins line-window chunks back into the original file,
// dropping the leading `overlap` lines of each chunk after the first (they
// duplicate the previous chunk's tail).
func reassembleChunks(chunks []string, overlap int) string {
	if len(chunks) == 1 {
		return chunks[0]
	}
	var b strings.Builder
	b.WriteString(chunks[0])
	for _, c := range chunks[1:] {
		lines := strings.Split(c, "\n")
		if overlap > 0 && len(lines) > overlap {
			lines = lines[overlap:]
		} else if overlap > 0 {
			lines = nil // wholly within the overlap → nothing new
		}
		if len(lines) > 0 {
			b.WriteString("\n")
			b.WriteString(strings.Join(lines, "\n"))
		}
	}
	return b.String()
}
