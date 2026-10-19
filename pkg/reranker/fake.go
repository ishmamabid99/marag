package reranker

import (
	"context"
	"strings"
)

var _ Reranker = (*Fake)(nil)

// Fake is a deterministic, dependency-free Reranker for tests and local runs.
// It scores a doc by how many distinct query terms it contains.
type Fake struct{}

// NewFake returns a Fake reranker.
func NewFake() *Fake { return &Fake{} }

func (f *Fake) Model() string { return "fake-rerank" }

// Passthrough reports that Fake provides no genuine semantic signal — its
// lexical token-overlap score is weak (often 0/1 for code). Callers should
// detect this and rank by vector distance (the real semantic signal) instead.
func (f *Fake) Passthrough() bool { return true }

// Rerank scores each doc by the count of distinct lowercased query tokens that
// occur (as whitespace-delimited tokens) in the doc.
func (f *Fake) Rerank(_ context.Context, query string, docs []string) ([]float64, error) {
	queryTerms := uniqueTokens(query)
	scores := make([]float64, len(docs))
	for i, doc := range docs {
		docTerms := tokenSet(doc)
		var n float64
		for term := range queryTerms {
			if _, ok := docTerms[term]; ok {
				n++
			}
		}
		scores[i] = n
	}
	return scores, nil
}

func uniqueTokens(s string) map[string]struct{} { return tokenSet(s) }

func tokenSet(s string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, tok := range strings.Fields(strings.ToLower(s)) {
		out[tok] = struct{}{}
	}
	return out
}
