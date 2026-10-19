package linear

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPClientPostComment(t *testing.T) {
	var gotAuth string
	var gotVars map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.Unmarshal(body, &req)
		gotVars = req.Variables
		if !strings.Contains(req.Query, "commentCreate") {
			t.Errorf("query = %s", req.Query)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"commentCreate": map[string]any{"success": true}}})
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.Client(), srv.URL, "lin_api_key")
	if err := c.PostComment(context.Background(), "iss-1", "hello"); err != nil {
		t.Fatalf("PostComment: %v", err)
	}
	if gotAuth != "lin_api_key" {
		t.Fatalf("auth = %q", gotAuth)
	}
	if gotVars["id"] != "iss-1" || gotVars["body"] != "hello" {
		t.Fatalf("vars = %+v", gotVars)
	}
}

func TestHTTPClientCommentCreateNotSuccessfulIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"commentCreate": map[string]any{"success": false}}})
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.Client(), srv.URL, "k")
	if err := c.PostComment(context.Background(), "i", "b"); err == nil {
		t.Fatalf("expected error when commentCreate.success is false")
	}
}

func TestHTTPClientGraphQLErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]any{{"message": "bad"}}})
	}))
	defer srv.Close()
	c := NewHTTPClient(srv.Client(), srv.URL, "k")
	if err := c.PostComment(context.Background(), "i", "b"); err == nil {
		t.Fatalf("expected error on graphql errors")
	}
}
