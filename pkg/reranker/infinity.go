package reranker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

var _ Reranker = (*Infinity)(nil)

// Infinity is a Reranker backed by an Infinity server's /rerank endpoint
// (michaelf34/infinity). Unlike TEI it publishes multi-arch images, so it runs
// natively on arm64. Its API differs from TEI: request uses "documents", the
// response wraps results and uses "relevance_score".
type Infinity struct {
	baseURL string
	model   string
	client  *http.Client
}

// NewInfinity returns an Infinity reranker targeting baseURL.
func NewInfinity(baseURL, model string) *Infinity {
	return &Infinity{baseURL: baseURL, model: model, client: &http.Client{Timeout: 60 * time.Second}}
}

func (i *Infinity) Model() string { return i.model }

type infinityRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

type infinityResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

type infinityResponse struct {
	Results []infinityResult `json:"results"`
}

// Rerank calls /rerank with all docs and realigns the returned relevance scores
// to the input order.
func (i *Infinity) Rerank(ctx context.Context, query string, docs []string) ([]float64, error) {
	if len(docs) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(infinityRequest{Model: i.model, Query: query, Documents: docs})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.baseURL+"/rerank", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := i.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call reranker: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("reranker status %d", resp.StatusCode)
	}

	var out infinityResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(out.Results) != len(docs) {
		return nil, fmt.Errorf("reranker returned %d results for %d docs", len(out.Results), len(docs))
	}

	scores := make([]float64, len(docs))
	seen := make([]bool, len(docs))
	for _, r := range out.Results {
		if r.Index < 0 || r.Index >= len(docs) {
			return nil, fmt.Errorf("reranker returned out-of-range index %d", r.Index)
		}
		scores[r.Index] = r.RelevanceScore
		seen[r.Index] = true
	}
	for idx, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("reranker missing score for index %d", idx)
		}
	}
	return scores, nil
}

// New builds a Reranker from config: empty baseURL → in-process fake; kind
// "infinity" → Infinity; otherwise the TEI-style HTTP reranker.
func New(kind, baseURL, model string) Reranker {
	if baseURL == "" {
		return NewFake()
	}
	if model == "" {
		model = "bge-reranker-base"
	}
	if kind == "infinity" {
		return NewInfinity(baseURL, model)
	}
	return NewHTTP(baseURL, model)
}
