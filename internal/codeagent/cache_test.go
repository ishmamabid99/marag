package codeagent

import (
	"context"
	"testing"
)

type countingSearcher struct {
	hits  []SearchHit
	calls int
}

func (c *countingSearcher) Search(_ context.Context, _ string, _ []string, _ int) ([]SearchHit, error) {
	c.calls++
	return c.hits, nil
}

func TestCachingSearcherMemoizes(t *testing.T) {
	inner := &countingSearcher{hits: []SearchHit{{RepoID: "a", Path: "x.go", Score: 0.9}}}
	c := NewCachingSearcher(inner)
	ctx := context.Background()

	h1, _ := c.Search(ctx, "currency", []string{"a", "b"}, 8)
	h2, _ := c.Search(ctx, "currency", []string{"a", "b"}, 8)
	if inner.calls != 1 {
		t.Fatalf("inner called %d times, want 1 (cached)", inner.calls)
	}
	if len(h1) != 1 || len(h2) != 1 || h2[0].Path != "x.go" {
		t.Fatalf("results differ: %+v %+v", h1, h2)
	}

	// Different query / repos / k → cache miss.
	_, _ = c.Search(ctx, "other", []string{"a", "b"}, 8)
	_, _ = c.Search(ctx, "currency", []string{"a"}, 8)
	_, _ = c.Search(ctx, "currency", []string{"a", "b"}, 3)
	if inner.calls != 4 {
		t.Fatalf("inner called %d times, want 4", inner.calls)
	}
}

func TestCachingSearcherDoesNotCacheErrors(t *testing.T) {
	inner := &errSearcher{}
	c := NewCachingSearcher(inner)
	if _, err := c.Search(context.Background(), "q", nil, 8); err == nil {
		t.Fatalf("expected error")
	}
	if _, err := c.Search(context.Background(), "q", nil, 8); err == nil {
		t.Fatalf("expected error on retry (errors not cached)")
	}
	if inner.calls != 2 {
		t.Fatalf("inner called %d, want 2 (error not cached)", inner.calls)
	}
}

type errSearcher struct{ calls int }

func (e *errSearcher) Search(_ context.Context, _ string, _ []string, _ int) ([]SearchHit, error) {
	e.calls++
	return nil, context.DeadlineExceeded
}
