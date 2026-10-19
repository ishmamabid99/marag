package codeagent

import (
	"context"
	"sync"
)

// SearchHit is one cross-repo retrieval result surfaced to the coding agent.
type SearchHit struct {
	RepoID  string
	Path    string
	Snippet string
	Score   float64
}

// ContextSearcher runs cross-repo semantic search (adapter over the retrieval-gateway).
type ContextSearcher interface {
	Search(ctx context.Context, query string, repoIDs []string, k int) ([]SearchHit, error)
}

// FakeSearcher is a ContextSearcher for tests.
type FakeSearcher struct {
	Hits    []SearchHit
	Err     error
	mu      sync.Mutex
	Queries []string
}

// Search records the query and returns the configured hits/err.
func (f *FakeSearcher) Search(_ context.Context, query string, _ []string, _ int) ([]SearchHit, error) {
	f.mu.Lock()
	f.Queries = append(f.Queries, query)
	f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Hits, nil
}
