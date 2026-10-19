package retrieval

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/fin/multi-agent-rag/internal/ingestion"
	"github.com/fin/multi-agent-rag/pkg/embedder"
	"github.com/fin/multi-agent-rag/pkg/reranker"
)

func TestReassembleChunks(t *testing.T) {
	// Single chunk → returned as-is.
	if got := reassembleChunks([]string{"a\nb\nc"}, 10); got != "a\nb\nc" {
		t.Fatalf("single chunk: %q", got)
	}
	// Two chunks with 2-line overlap: chunk2's first 2 lines duplicate chunk1's
	// tail and must be dropped so the file reconstructs exactly once.
	got := reassembleChunks([]string{"l1\nl2\nl3\nl4", "l3\nl4\nl5\nl6"}, 2)
	if want := "l1\nl2\nl3\nl4\nl5\nl6"; got != want {
		t.Fatalf("reassembled = %q, want %q", got, want)
	}
}

func TestGatewayGetFile(t *testing.T) {
	ctx := context.Background()
	pool, svc := newTestDB(t, "gf1")
	ing := ingestion.NewIngestor(pool, svc, ingestion.NewVectorWriter(pool), embedder.NewFake(768))
	if _, err := ing.Ingest(ctx, "gf1", map[string]string{"a.go": "package x\n\nfunc A() int { return 42 }\n"}, 0); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	g := NewGateway(pool, svc, embedder.NewFake(768), reranker.NewFake())

	got, found, err := g.GetFile(ctx, "gf1", "a.go")
	if err != nil || !found {
		t.Fatalf("GetFile: found=%v err=%v", found, err)
	}
	if !strings.Contains(got, "func A() int { return 42 }") {
		t.Fatalf("GetFile content missing body: %q", got)
	}
	if _, found, _ := g.GetFile(ctx, "gf1", "nope.go"); found {
		t.Fatal("unknown file must report found=false")
	}
}

// With the fake (passthrough) reranker, results must be ranked by vector
// distance (the semantic signal), and Score must be 1 - distance — not the
// fake's lexical 0/1 counts. This is the fix for "same results / 0-1 scores".
func TestGatewayPassthroughRanksByDistance(t *testing.T) {
	ctx := context.Background()
	pool, svc := newTestDB(t, "pt1")
	ing := ingestion.NewIngestor(pool, svc, ingestion.NewVectorWriter(pool), embedder.NewFake(768))
	files := map[string]string{"a.go": "alpha alpha", "b.go": "beta beta", "c.go": "gamma delta"}
	if _, err := ing.Ingest(ctx, "pt1", files, 0); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	g := NewGateway(pool, svc, embedder.NewFake(768), reranker.NewFake())
	resp, err := g.Search(ctx, SearchRequest{
		Query: "alpha", RepoIDs: []string{"pt1"}, PerRepoBudget: 100, FinalTopK: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) < 2 {
		t.Fatalf("want >=2 results, got %d", len(resp.Results))
	}
	for i, r := range resp.Results {
		if math.Abs(r.Score-(1-r.Distance)) > 1e-9 {
			t.Fatalf("result %d: score=%v want 1-distance=%v (passthrough scoring)", i, r.Score, 1-r.Distance)
		}
		if i > 0 && resp.Results[i-1].Distance > r.Distance+1e-9 {
			t.Fatalf("results not ordered by ascending distance: %v before %v", resp.Results[i-1].Distance, r.Distance)
		}
	}
}

// errReranker is a non-passthrough reranker that always fails — used to prove
// retrieval degrades to distance ranking instead of failing the whole query.
type errReranker struct{ mode string }

func (errReranker) Model() string { return "err-rerank" }
func (e errReranker) Rerank(_ context.Context, _ string, docs []string) ([]float64, error) {
	if e.mode == "wrongcount" {
		return make([]float64, len(docs)+1), nil // valid err path: mismatched count
	}
	return nil, fmt.Errorf("reranker connection refused")
}

// A real reranker that errors (or returns the wrong score count) must NOT fail
// the search — retrieval degrades to vector-distance ranking (score=1-distance),
// because the reranker is a quality enhancement, not a correctness requirement.
func TestGatewayRerankerFailureFallsBackToDistance(t *testing.T) {
	for _, mode := range []string{"error", "wrongcount"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			pool, svc := newTestDB(t, "fb-"+mode)
			ing := ingestion.NewIngestor(pool, svc, ingestion.NewVectorWriter(pool), embedder.NewFake(768))
			files := map[string]string{"a.go": "alpha alpha", "b.go": "beta beta", "c.go": "gamma delta"}
			if _, err := ing.Ingest(ctx, "fb-"+mode, files, 0); err != nil {
				t.Fatalf("Ingest: %v", err)
			}
			g := NewGateway(pool, svc, embedder.NewFake(768), errReranker{mode: mode})
			resp, err := g.Search(ctx, SearchRequest{
				Query: "alpha", RepoIDs: []string{"fb-" + mode}, PerRepoBudget: 100, FinalTopK: 10,
			})
			if err != nil {
				t.Fatalf("Search must not fail when the reranker fails: %v", err)
			}
			if len(resp.Results) < 2 {
				t.Fatalf("want >=2 results, got %d", len(resp.Results))
			}
			for i, r := range resp.Results {
				if math.Abs(r.Score-(1-r.Distance)) > 1e-9 {
					t.Fatalf("result %d: score=%v want 1-distance=%v (distance fallback)", i, r.Score, 1-r.Distance)
				}
				if i > 0 && resp.Results[i-1].Distance > r.Distance+1e-9 {
					t.Fatalf("fallback results not ordered by ascending distance")
				}
			}
		})
	}
}

func TestGatewaySearchEndToEnd(t *testing.T) {
	ctx := context.Background()
	pool, svc := newTestDB(t, "repoA")

	// Ingest real files with the Fake embedder (random-but-deterministic vectors).
	ing := ingestion.NewIngestor(pool, svc, ingestion.NewVectorWriter(pool), embedder.NewFake(768))
	files := map[string]string{
		"pay.go":  "func ProcessPayment() { validate currency amounts }",
		"util.go": "func helper() { unrelated weather logic }",
		"auth.go": "func Login() { session tokens }",
	}
	if _, err := ing.Ingest(ctx, "repoA", files, 0); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	g := NewGateway(pool, svc, embedder.NewFake(768), reranker.NewFake())
	// Large PerRepoBudget so all chunks are candidates regardless of the Fake
	// embedder's random vector distances; the reranker then orders by term overlap.
	resp, err := g.Search(ctx, SearchRequest{
		Query: "validate currency payment", RepoIDs: []string{"repoA"},
		PerRepoBudget: 100, FinalTopK: 3,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("no results")
	}
	// The payment/currency chunk must rank first after reranking.
	if resp.Results[0].FilePath != "pay.go" {
		t.Fatalf("top result = %q, want pay.go; results=%+v", resp.Results[0].FilePath, resp.Results)
	}
	// Staleness reported for the repo; it was just ingested → not stale.
	if len(resp.Staleness) != 1 || resp.Staleness[0].RepoID != "repoA" {
		t.Fatalf("staleness = %+v", resp.Staleness)
	}
	if resp.Staleness[0].Stale {
		t.Fatal("freshly ingested repo should not be stale")
	}
}

func TestGatewaySkipsUnknownAndNeverIngested(t *testing.T) {
	ctx := context.Background()
	pool, svc := newTestDB(t, "repoB") // registered but never ingested → ActiveVersion 0

	g := NewGateway(pool, svc, embedder.NewFake(768), reranker.NewFake())
	resp, err := g.Search(ctx, SearchRequest{
		Query: "anything", RepoIDs: []string{"repoB", "ghost"}, FinalTopK: 5,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// No vectors anywhere → no results, no error.
	if len(resp.Results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(resp.Results))
	}
	// repoB is reported in staleness (registered, never ingested → stale); ghost is skipped.
	if len(resp.Staleness) != 1 || resp.Staleness[0].RepoID != "repoB" || !resp.Staleness[0].Stale {
		t.Fatalf("staleness = %+v", resp.Staleness)
	}
}

func TestGatewayDimensionMismatch(t *testing.T) {
	ctx := context.Background()
	pool, svc := newTestDB(t, "repoC")
	g := NewGateway(pool, svc, embedder.NewFake(128), reranker.NewFake()) // 128 != repo 768
	_, err := g.Search(ctx, SearchRequest{Query: "q", RepoIDs: []string{"repoC"}})
	if err == nil {
		t.Fatal("expected dimension-mismatch error")
	}
}
