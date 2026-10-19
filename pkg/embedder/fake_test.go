package embedder

import (
	"context"
	"math"
	"testing"
)

func TestFakeDeterministicAndShaped(t *testing.T) {
	f := NewFake(768)
	if f.Dimension() != 768 {
		t.Fatalf("Dimension = %d, want 768", f.Dimension())
	}
	if f.Model() != "fake-embed" {
		t.Fatalf("Model = %q", f.Model())
	}

	ctx := context.Background()
	out, err := f.Embed(ctx, []string{"hello world", "hello world", "different"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d vectors, want 3", len(out))
	}
	for i, v := range out {
		if len(v) != 768 {
			t.Fatalf("vector %d len = %d, want 768", i, len(v))
		}
	}

	// Deterministic: identical input → identical output.
	for j := range out[0] {
		if out[0][j] != out[1][j] {
			t.Fatalf("same text produced different vectors at %d", j)
		}
	}

	// Distinct input → distinct output.
	same := true
	for j := range out[0] {
		if out[0][j] != out[2][j] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("different texts produced identical vectors")
	}

	// L2-normalized (unit length) so cosine distance is well-behaved.
	var norm float64
	for _, x := range out[0] {
		norm += float64(x) * float64(x)
	}
	if math.Abs(math.Sqrt(norm)-1.0) > 1e-4 {
		t.Fatalf("vector not unit-normalized: norm=%f", math.Sqrt(norm))
	}
}

func TestFakeEmptyInput(t *testing.T) {
	out, err := NewFake(768).Embed(context.Background(), nil)
	if err != nil {
		t.Fatalf("Embed(nil): %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("Embed(nil) len = %d, want 0", len(out))
	}
}
