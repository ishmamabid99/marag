// Package retrieval is the GraphRAG query engine: it fans out a query across
// per-repo pgvector partitions, reranks the merged candidates, and returns
// results with staleness metadata.
package retrieval

import (
	"context"
	"fmt"

	"github.com/fin/multi-agent-rag/internal/pgvec"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Candidate is one retrieved chunk before reranking.
type Candidate struct {
	RepoID     string
	FilePath   string
	ChunkIndex int
	Content    string
	Kind       string  // "doc" | "code" (from code_embeddings.kind)
	Distance   float64 // cosine distance; lower = more similar
}

type searcher struct {
	pool *pgxpool.Pool
}

func newSearcher(pool *pgxpool.Pool) *searcher { return &searcher{pool: pool} }

// fileChunks returns a file's chunk contents in chunk_index order at the given
// version (empty if the file isn't indexed).
func (s *searcher) fileChunks(ctx context.Context, repoID, filePath string, version int) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT content FROM code_embeddings
		 WHERE repo_id=$1 AND file_path=$2 AND version=$3
		 ORDER BY chunk_index`,
		repoID, filePath, version)
	if err != nil {
		return nil, fmt.Errorf("file chunks %s/%s: %w", repoID, filePath, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// topK returns the k nearest chunks (ascending cosine distance) within one
// repo's partition at the given version.
func (s *searcher) topK(ctx context.Context, repoID string, version, k int, qvec []float32) ([]Candidate, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT file_path, chunk_index, content, kind, embedding <=> $1::vector AS distance
		 FROM code_embeddings
		 WHERE repo_id = $2 AND version = $3
		 ORDER BY embedding <=> $1::vector
		 LIMIT $4`,
		pgvec.Literal(qvec), repoID, version, k)
	if err != nil {
		return nil, fmt.Errorf("query repo %s: %w", repoID, err)
	}
	defer rows.Close()

	var out []Candidate
	for rows.Next() {
		c := Candidate{RepoID: repoID}
		if err := rows.Scan(&c.FilePath, &c.ChunkIndex, &c.Content, &c.Kind, &c.Distance); err != nil {
			return nil, fmt.Errorf("scan candidate: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
