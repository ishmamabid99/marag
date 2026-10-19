package gitprovider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGitLabCreateMRAndDefaultBranch(t *testing.T) {
	var gotCreate map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.EscapedPath() == "/projects/o%2Fr":
			_ = json.NewEncoder(w).Encode(map[string]any{"default_branch": "main"})
		case r.Method == "POST" && r.URL.EscapedPath() == "/projects/o%2Fr/merge_requests":
			if r.Header.Get("PRIVATE-TOKEN") != "tok" {
				t.Errorf("token header = %q", r.Header.Get("PRIVATE-TOKEN"))
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &gotCreate)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"iid": 7, "web_url": "https://gitlab.com/o/r/-/merge_requests/7"})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.EscapedPath())
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()

	gl := NewGitLab(srv.Client())
	repo := RepoRef{Owner: "o", Name: "r", APIBaseURL: srv.URL, Token: "tok"}
	ctx := context.Background()

	if db, err := gl.GetDefaultBranch(ctx, repo); err != nil || db != "main" {
		t.Fatalf("default branch = %q err=%v", db, err)
	}
	res, err := gl.CreatePR(ctx, repo, PRRequest{Title: "T", Body: "B", SourceBranch: "feat/x", TargetBranch: "main"})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if res.Number != 7 || res.ID != "7" || !strings.Contains(res.URL, "merge_requests/7") {
		t.Fatalf("result = %+v", res)
	}
	if gotCreate["source_branch"] != "feat/x" || gotCreate["target_branch"] != "main" || gotCreate["title"] != "T" {
		t.Fatalf("create body = %+v", gotCreate)
	}
}

func TestGitLabCloseAndDeleteBranch(t *testing.T) {
	var closed bool
	var deletedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PUT" && r.URL.EscapedPath() == "/projects/o%2Fr/merge_requests/7":
			body, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			if m["state_event"] == "close" {
				closed = true
			}
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.EscapedPath(), "/projects/o%2Fr/repository/branches/"):
			deletedPath = r.URL.EscapedPath()
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.EscapedPath())
		}
	}))
	defer srv.Close()

	gl := NewGitLab(srv.Client())
	repo := RepoRef{Owner: "o", Name: "r", APIBaseURL: srv.URL, Token: "tok"}
	ctx := context.Background()
	if err := gl.ClosePR(ctx, repo, "7"); err != nil || !closed {
		t.Fatalf("ClosePR err=%v closed=%v", err, closed)
	}
	if err := gl.DeleteBranch(ctx, repo, "feat/x"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	if !strings.HasSuffix(deletedPath, "/feat/x") {
		t.Fatalf("deleted path = %q", deletedPath)
	}
}
