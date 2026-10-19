package codeagent

import (
	"context"
	"strconv"
	"strings"
	"sync"
)

// CachingSearcher memoizes non-error ContextSearcher results by (query, repoIDs, k)
// for its lifetime. Intended per-run: cheap to construct, no eviction (a run is
// short-lived). Concurrency-safe.
type CachingSearcher struct {
	inner ContextSearcher
	mu    sync.Mutex
	cache map[string][]SearchHit
}

// NewCachingSearcher wraps inner with an in-memory cache.
func NewCachingSearcher(inner ContextSearcher) *CachingSearcher {
	return &CachingSearcher{inner: inner, cache: map[string][]SearchHit{}}
}

// Search returns a cached result when present, else delegates and caches success.
func (c *CachingSearcher) Search(ctx context.Context, query string, repoIDs []string, k int) ([]SearchHit, error) {
	key := query + "\x00" + strconv.Itoa(k) + "\x00" + strings.Join(repoIDs, ",")
	c.mu.Lock()
	if hits, ok := c.cache[key]; ok {
		c.mu.Unlock()
		return hits, nil
	}
	c.mu.Unlock()

	hits, err := c.inner.Search(ctx, query, repoIDs, k)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.cache[key] = hits
	c.mu.Unlock()
	return hits, nil
}
