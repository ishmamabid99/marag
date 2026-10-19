// Package reranker defines the pluggable cross-encoder reranker interface and
// implementations (a deterministic fake for tests and a TEI-style HTTP backend).
package reranker

import "context"

// Reranker scores each document's relevance to a query. Confined to ranking —
// never a reasoning stage.
type Reranker interface {
	// Rerank returns one score per doc, aligned to the input order. Higher =
	// more relevant. len(result) == len(docs).
	Rerank(ctx context.Context, query string, docs []string) ([]float64, error)
	// Model is the backend model identifier.
	Model() string
}
