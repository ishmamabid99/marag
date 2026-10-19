package ingestion

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// flakyEmbedder fails on any text containing failOn, to prove ingestion skips
// an unembeddable chunk instead of aborting the whole repo.
type flakyEmbedder struct{ failOn string }

func (f flakyEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	for _, t := range texts {
		if strings.Contains(t, f.failOn) {
			return nil, errors.New("embedder rejected input")
		}
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, 768)
	}
	return out, nil
}
func (flakyEmbedder) Dimension() int { return 768 }
func (flakyEmbedder) Model() string  { return "flaky" }

func TestChunkAndEmbedSkipsRejectedChunks(t *testing.T) {
	ing := &Ingestor{emb: flakyEmbedder{failOn: "POISON"}, chunk: DefaultChunkOptions()}
	files := map[string]string{
		"good.go": "package a\n\nfunc A() int { return 1 }\n",
		"bad.go":  "POISON this chunk cannot be embedded\n",
	}
	out, err := ing.chunkAndEmbed(context.Background(), files)
	if err != nil {
		t.Fatalf("one bad chunk must not abort the ingest: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("the good chunk should still be embedded")
	}
	for _, e := range out {
		if strings.Contains(e.Chunk.Content, "POISON") {
			t.Fatal("the rejected chunk should have been skipped")
		}
	}
}
