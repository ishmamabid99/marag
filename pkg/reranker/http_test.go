package reranker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPRerankRealignsByIndex(t *testing.T) {
	var gotQuery string
	var gotTexts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" {
			t.Errorf("path = %q, want /rerank", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query string   `json:"query"`
			Texts []string `json:"texts"`
		}
		_ = json.Unmarshal(body, &req)
		gotQuery, gotTexts = req.Query, req.Texts
		// Return results OUT OF ORDER (sorted by score desc) to exercise realignment.
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"index": 2, "score": 0.9},
			{"index": 0, "score": 0.5},
			{"index": 1, "score": 0.1},
		})
	}))
	defer srv.Close()

	rr := NewHTTP(srv.URL, "bge-reranker")
	if rr.Model() != "bge-reranker" {
		t.Fatalf("Model = %q", rr.Model())
	}
	scores, err := rr.Rerank(context.Background(), "q", []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	// Aligned back to input order [a,b,c] = [index0,index1,index2].
	want := []float64{0.5, 0.1, 0.9}
	for i := range want {
		if scores[i] != want[i] {
			t.Fatalf("scores = %v, want %v", scores, want)
		}
	}
	if gotQuery != "q" || len(gotTexts) != 3 {
		t.Fatalf("request query=%q texts=%v", gotQuery, gotTexts)
	}
}

func TestHTTPRerankServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := NewHTTP(srv.URL, "m").Rerank(context.Background(), "q", []string{"a"}); err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestHTTPRerankEmptyDocs(t *testing.T) {
	// No HTTP call needed for empty docs; must return empty without error.
	s, err := NewHTTP("http://unused.invalid", "m").Rerank(context.Background(), "q", nil)
	if err != nil {
		t.Fatalf("Rerank(nil): %v", err)
	}
	if len(s) != 0 {
		t.Fatalf("len = %d, want 0", len(s))
	}
}
