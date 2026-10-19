// Package embedder defines the pluggable embedding backend interface and
// implementations (a deterministic fake for tests and an Ollama HTTP backend).
package embedder

import (
	"context"
	"errors"
)

// ErrDimensionMismatch is returned when a backend produces a vector whose
// length does not match its declared Dimension().
var ErrDimensionMismatch = errors.New("embedder: dimension mismatch")

// Embedder turns text into fixed-dimension vectors. Confined to embeddings —
// never a reasoning stage.
type Embedder interface {
	// Embed returns one vector per input text, in order. Length of the result
	// equals len(texts); each vector has length Dimension().
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dimension is the fixed output vector length.
	Dimension() int
	// Model is the backend model identifier (stored per repo in the registry).
	Model() string
}
