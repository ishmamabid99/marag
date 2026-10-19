package embedder

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaEmbed(t *testing.T) {
	var gotModel, gotPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" {
			t.Errorf("path = %q, want /api/embeddings", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
		}
		_ = json.Unmarshal(body, &req)
		gotModel, gotPrompt = req.Model, req.Prompt
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float64{0.1, 0.2, 0.3}})
	}))
	defer srv.Close()

	e := NewOllama(srv.URL, "nomic-embed-text", 3)
	if e.Model() != "nomic-embed-text" || e.Dimension() != 3 {
		t.Fatalf("Model/Dimension = %q/%d", e.Model(), e.Dimension())
	}

	out, err := e.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(out) != 1 || len(out[0]) != 3 {
		t.Fatalf("shape = %d x %v", len(out), out)
	}
	if out[0][0] != 0.1 || out[0][2] != 0.3 {
		t.Fatalf("values = %v", out[0])
	}
	if gotModel != "nomic-embed-text" || gotPrompt != "hello" {
		t.Fatalf("request had model=%q prompt=%q", gotModel, gotPrompt)
	}
}

func TestOllamaDimensionMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float64{0.1, 0.2}})
	}))
	defer srv.Close()

	_, err := NewOllama(srv.URL, "m", 3).Embed(context.Background(), []string{"x"})
	if !errors.Is(err, ErrDimensionMismatch) {
		t.Fatalf("err = %v, want ErrDimensionMismatch", err)
	}
}

func TestOllamaServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := NewOllama(srv.URL, "m", 3).Embed(context.Background(), []string{"x"})
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
}
