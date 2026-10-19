package reranker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

var _ Reranker = (*HTTP)(nil)

// HTTP is a Reranker backed by a TEI-style /rerank endpoint (e.g. a local
// bge-reranker served by Text Embeddings Inference).
type HTTP struct {
	baseURL string
	model   string
	client  *http.Client
}

// NewHTTP returns an HTTP reranker targeting baseURL.
func NewHTTP(baseURL, model string) *HTTP {
	return &HTTP{baseURL: baseURL, model: model, client: &http.Client{Timeout: 60 * time.Second}}
}

func (h *HTTP) Model() string { return h.model }

type rerankRequest struct {
	Query string   `json:"query"`
	Texts []string `json:"texts"`
}

type rerankResult struct {
	Index int     `json:"index"`
	Score float64 `json:"score"`
}

// Rerank calls the endpoint once with all docs and realigns the returned
// scores to the input order.
func (h *HTTP) Rerank(ctx context.Context, query string, docs []string) ([]float64, error) {
	if len(docs) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(rerankRequest{Query: query, Texts: docs})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+"/rerank", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call reranker: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("reranker status %d", resp.StatusCode)
	}

	var results []rerankResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(results) != len(docs) {
		return nil, fmt.Errorf("reranker returned %d results for %d docs", len(results), len(docs))
	}

	scores := make([]float64, len(docs))
	seen := make([]bool, len(docs))
	for _, r := range results {
		if r.Index < 0 || r.Index >= len(docs) {
			return nil, fmt.Errorf("reranker returned out-of-range index %d", r.Index)
		}
		scores[r.Index] = r.Score
		seen[r.Index] = true
	}
	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("reranker missing score for index %d", i)
		}
	}
	return scores, nil
}
