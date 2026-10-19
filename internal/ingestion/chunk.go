// Package ingestion turns repository files into versioned pgvector embeddings
// and drives the registry's atomic active-version swap.
package ingestion

import "strings"

// Chunk is a contiguous slice of a file's lines.
type Chunk struct {
	FilePath string
	Index    int
	Content  string
	Kind     ChunkKind // doc | code (classified from FilePath)
}

// ChunkOptions controls the line-window chunker.
type ChunkOptions struct {
	LinesPerChunk int
	OverlapLines  int
}

// DefaultChunkOptions returns sane defaults for source code.
func DefaultChunkOptions() ChunkOptions {
	return ChunkOptions{LinesPerChunk: 60, OverlapLines: 10}
}

// ChunkFile splits content into overlapping line windows. Whitespace-only or
// empty content yields no chunks. A file shorter than one window yields one
// chunk. Invalid options are clamped to safe values.
func ChunkFile(filePath, content string, opts ChunkOptions) []Chunk {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	if opts.LinesPerChunk <= 0 {
		opts.LinesPerChunk = DefaultChunkOptions().LinesPerChunk
	}
	if opts.OverlapLines < 0 || opts.OverlapLines >= opts.LinesPerChunk {
		opts.OverlapLines = 0
	}
	stride := opts.LinesPerChunk - opts.OverlapLines

	content = strings.TrimSuffix(content, "\n")
	lines := strings.Split(content, "\n")
	var chunks []Chunk
	idx := 0
	for start := 0; start < len(lines); start += stride {
		end := start + opts.LinesPerChunk
		if end > len(lines) {
			end = len(lines)
		}
		chunks = append(chunks, Chunk{
			FilePath: filePath,
			Index:    idx,
			Content:  strings.Join(lines[start:end], "\n"),
			Kind:     KindForPath(filePath),
		})
		idx++
		if end == len(lines) {
			break
		}
	}
	return chunks
}
