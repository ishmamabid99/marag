package ingestion

import (
	"strings"
	"testing"
)

func TestChunkFileSmall(t *testing.T) {
	got := ChunkFile("a.go", "line1\nline2\nline3", ChunkOptions{LinesPerChunk: 60, OverlapLines: 10})
	if len(got) != 1 {
		t.Fatalf("small file → %d chunks, want 1", len(got))
	}
	if got[0].FilePath != "a.go" || got[0].Index != 0 {
		t.Fatalf("chunk meta = %+v", got[0])
	}
	if got[0].Content != "line1\nline2\nline3" {
		t.Fatalf("content = %q", got[0].Content)
	}
}

func TestChunkFileWindows(t *testing.T) {
	content := strings.Repeat("l\n", 99) + "l" // 100 real lines, no trailing newline
	got := ChunkFile("big.go", content, ChunkOptions{LinesPerChunk: 40, OverlapLines: 10})
	// stride = 40 - 10 = 30. Starts at 0,30,60 → 3 chunks over 100 lines
	// (the window at start=60 reaches end=100=len and breaks).
	if len(got) != 3 {
		t.Fatalf("got %d chunks, want 3", len(got))
	}
	for i, c := range got {
		if c.Index != i {
			t.Fatalf("chunk %d has Index %d", i, c.Index)
		}
	}
	// Overlap: last 10 lines of chunk 0 reappear as first 10 lines of chunk 1.
	c0 := strings.Split(got[0].Content, "\n")
	c1 := strings.Split(got[1].Content, "\n")
	if strings.Join(c0[30:40], "\n") != strings.Join(c1[0:10], "\n") {
		t.Fatal("expected 10-line overlap between consecutive chunks")
	}
}

func TestChunkFileExactWindowSingleChunk(t *testing.T) {
	opts := ChunkOptions{LinesPerChunk: 3, OverlapLines: 1}

	got := ChunkFile("f.go", "a\nb\nc", opts)
	if len(got) != 1 {
		t.Fatalf("no trailing newline → %d chunks, want 1", len(got))
	}
	if got[0].Content != "a\nb\nc" {
		t.Fatalf("content = %q, want %q", got[0].Content, "a\nb\nc")
	}

	got = ChunkFile("f.go", "a\nb\nc\n", opts)
	if len(got) != 1 {
		t.Fatalf("trailing newline → %d chunks, want 1", len(got))
	}
	if got[0].Content != "a\nb\nc" {
		t.Fatalf("content = %q, want %q", got[0].Content, "a\nb\nc")
	}
}

func TestChunkFileEmpty(t *testing.T) {
	if got := ChunkFile("e.go", "   \n  \n", DefaultChunkOptions()); len(got) != 0 {
		t.Fatalf("whitespace-only → %d chunks, want 0", len(got))
	}
	if got := ChunkFile("e.go", "", DefaultChunkOptions()); len(got) != 0 {
		t.Fatalf("empty → %d chunks, want 0", len(got))
	}
}

func TestDefaultChunkOptions(t *testing.T) {
	o := DefaultChunkOptions()
	if o.LinesPerChunk != 60 || o.OverlapLines != 10 {
		t.Fatalf("defaults = %+v", o)
	}
}
