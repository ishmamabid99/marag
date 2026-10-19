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

func TestGitHubCreatePRAndGetDefaultBranch(t *testing.T) {
	var gotCreate map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/o/r":
			_ = json.NewEncoder(w).Encode(map[string]any{"default_branch": "main"})
		case r.Method == "POST" && r.URL.Path == "/repos/o/r/pulls":
			if r.Header.Get("Authorization") != "Bearer tok" {
				t.Errorf("auth = %q", r.Header.Get("Authorization"))
			}
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &gotCreate)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 42, "html_url": "https://github.com/o/r/pull/42"})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()

	gh := NewGitHub(srv.Client())
	repo := RepoRef{Owner: "o", Name: "r", APIBaseURL: srv.URL, Token: "tok"}
	ctx := context.Background()

	db, err := gh.GetDefaultBranch(ctx, repo)
	if err != nil || db != "main" {
		t.Fatalf("default branch = %q err=%v", db, err)
	}
	res, err := gh.CreatePR(ctx, repo, PRRequest{Title: "T", Body: "B", SourceBranch: "feat/x", TargetBranch: "main"})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if res.Number != 42 || res.ID != "42" || !strings.Contains(res.URL, "pull/42") {
		t.Fatalf("result = %+v", res)
	}
	if gotCreate["head"] != "feat/x" || gotCreate["base"] != "main" || gotCreate["title"] != "T" {
		t.Fatalf("create body = %+v", gotCreate)
	}
}

func TestGitHubCloseAndDeleteBranch(t *testing.T) {
	var closed bool
	var deletedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PATCH" && r.URL.Path == "/repos/o/r/pulls/42":
			body, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			if m["state"] == "closed" {
				closed = true
			}
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/repos/o/r/git/refs/heads/"):
			deletedPath = r.URL.Path
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	gh := NewGitHub(srv.Client())
	repo := RepoRef{Owner: "o", Name: "r", APIBaseURL: srv.URL, Token: "tok"}
	ctx := context.Background()
	if err := gh.ClosePR(ctx, repo, "42"); err != nil || !closed {
		t.Fatalf("ClosePR err=%v closed=%v", err, closed)
	}
	if err := gh.DeleteBranch(ctx, repo, "feat/x"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	if !strings.HasSuffix(deletedPath, "/feat/x") {
		t.Fatalf("deleted path = %q", deletedPath)
	}
}

func TestGitHubNon2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"message":"validation failed"}`))
	}))
	defer srv.Close()
	gh := NewGitHub(srv.Client())
	repo := RepoRef{Owner: "o", Name: "r", APIBaseURL: srv.URL, Token: "tok"}
	_, err := gh.CreatePR(context.Background(), repo, PRRequest{SourceBranch: "x", TargetBranch: "main"})
	if err == nil || !strings.Contains(err.Error(), "422") {
		t.Fatalf("err = %v, want 422", err)
	}
}
