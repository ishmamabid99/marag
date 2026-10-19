package embedder

import (
	"context"
	"hash/fnv"
	"math"
)

// Fake is a deterministic, dependency-free Embedder for tests and local runs.
// Each text maps to a stable pseudo-random unit vector derived from its hash.
type Fake struct {
	dim int
}

// NewFake returns a Fake producing dim-length vectors.
func NewFake(dim int) *Fake { return &Fake{dim: dim} }

func (f *Fake) Dimension() int { return f.dim }
func (f *Fake) Model() string  { return "fake-embed" }

// Embed produces a deterministic L2-normalized vector per text.
func (f *Fake) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = f.vector(t)
	}
	return out, nil
}

func (f *Fake) vector(text string) []float32 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	// xorshift64 seeded by the text hash → deterministic pseudo-random stream.
	x := h.Sum64() | 1
	v := make([]float32, f.dim)
	var norm float64
	for j := 0; j < f.dim; j++ {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		val := float32(int64(x%2001)-1000) / 1000.0 // in [-1, 1]
		v[j] = val
		norm += float64(val) * float64(val)
	}
	if norm > 0 {
		inv := float32(1.0 / math.Sqrt(norm))
		for j := range v {
			v[j] *= inv
		}
	}
	return v
}
