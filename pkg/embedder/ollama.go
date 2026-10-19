package embedder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Ollama is an Embedder backed by an Ollama server's /api/embeddings endpoint.
type Ollama struct {
	baseURL string
	model   string
	dim     int
	client  *http.Client
}

// NewOllama returns an Ollama backend targeting baseURL with the given model
// and expected output dimension.
func NewOllama(baseURL, model string, dim int) *Ollama {
	return &Ollama{
		baseURL: baseURL,
		model:   model,
		dim:     dim,
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

func (o *Ollama) Dimension() int { return o.dim }
func (o *Ollama) Model() string  { return o.model }

type ollamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaResponse struct {
	Embedding []float64 `json:"embedding"`
}

// Embed calls the endpoint once per text, preserving order.
func (o *Ollama) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		vec, err := o.embedOne(ctx, t)
		if err != nil {
			return nil, fmt.Errorf("embed text %d: %w", i, err)
		}
		out[i] = vec
	}
	return out, nil
}

// maxEmbedChars caps the input length so an over-long chunk (e.g. a big
// prose/markdown line window) can't exceed the model's token limit, which
// Ollama rejects with a 500. Empirically nomic-embed-text 500s above ~4-6k
// chars, so this stays comfortably under.
const maxEmbedChars = 4000

func (o *Ollama) embedOne(ctx context.Context, text string) ([]float32, error) {
	if r := []rune(text); len(r) > maxEmbedChars {
		text = string(r[:maxEmbedChars])
	}
	body, err := json.Marshal(ollamaRequest{Model: o.model, Prompt: text})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.baseURL+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call ollama: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama status %d", resp.StatusCode)
	}

	var parsed ollamaResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(parsed.Embedding) != o.dim {
		return nil, fmt.Errorf("%w: got %d, want %d", ErrDimensionMismatch, len(parsed.Embedding), o.dim)
	}
	vec := make([]float32, o.dim)
	for j, x := range parsed.Embedding {
		vec[j] = float32(x)
	}
	return vec, nil
}
