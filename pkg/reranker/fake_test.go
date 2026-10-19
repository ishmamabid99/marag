package reranker

import (
	"context"
	"testing"
)

func TestFakeScoresByOverlap(t *testing.T) {
	f := NewFake()
	if f.Model() != "fake-rerank" {
		t.Fatalf("Model = %q", f.Model())
	}
	docs := []string{
		"unrelated text about weather",
		"payment validation for currency amounts",
		"a doc mentioning payment only",
	}
	scores, err := f.Rerank(context.Background(), "payment validation currency", docs)
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(scores) != 3 {
		t.Fatalf("got %d scores, want 3", len(scores))
	}
	// doc[1] has all 3 query terms → highest; doc[2] has 1 → middle; doc[0] → 0.
	if !(scores[1] > scores[2] && scores[2] > scores[0]) {
		t.Fatalf("expected scores[1] > scores[2] > scores[0], got %v", scores)
	}
	if scores[0] != 0 {
		t.Fatalf("unrelated doc should score 0, got %v", scores[0])
	}
}

func TestFakeDeterministic(t *testing.T) {
	f := NewFake()
	a, _ := f.Rerank(context.Background(), "q term", []string{"q term here", "nothing"})
	b, _ := f.Rerank(context.Background(), "q term", []string{"q term here", "nothing"})
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("nondeterministic at %d: %v vs %v", i, a, b)
		}
	}
}

func TestFakeEmptyDocs(t *testing.T) {
	s, err := NewFake().Rerank(context.Background(), "q", nil)
	if err != nil {
		t.Fatalf("Rerank(nil): %v", err)
	}
	if len(s) != 0 {
		t.Fatalf("len = %d, want 0", len(s))
	}
}
