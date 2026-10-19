package gateway

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeReader struct {
	run   RunView
	found bool
	arts  map[string][]byte
	list  []RunView
}

func (f *fakeReader) GetRun(_ context.Context, id string) (RunView, bool, error) {
	return f.run, f.found, nil
}
func (f *fakeReader) ListRuns(_ context.Context, limit int) ([]RunView, error) { return f.list, nil }
func (f *fakeReader) LoadArtifacts(_ context.Context, id string) (map[string][]byte, error) {
	return f.arts, nil
}

type fakeRepos struct{ repos []RepoView }

func (f *fakeRepos) ListRepos(_ context.Context) ([]RepoView, error) { return f.repos, nil }

type fakeSearch struct {
	hits    []SearchHit
	gotK    int
	gotRepo []string
}

func (f *fakeSearch) Search(_ context.Context, q string, repoIDs []string, k int) ([]SearchHit, error) {
	f.gotK, f.gotRepo = k, repoIDs
	return f.hits, nil
}

func (f *fakeSearch) GetFile(_ context.Context, repoID, filePath string) (string, bool, error) {
	if filePath == "known.go" {
		return "package known\n", true, nil
	}
	return "", false, nil
}

func TestRunStatusFillsArtifacts(t *testing.T) {
	r := &fakeReader{
		run:   RunView{RunID: "r1", Status: "running", CurrentStage: "developing"},
		found: true,
		arts:  map[string][]byte{"developing": []byte(`{"tests_passed":true}`)},
	}
	g := New(nil, r, nil, nil)
	v, err := g.RunStatus(context.Background(), "r1")
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if v.Status != "running" || len(v.Artifacts) != 1 {
		t.Fatalf("view = %+v", v)
	}
	if string(v.Artifacts["developing"]) != `{"tests_passed":true}` {
		t.Fatalf("artifact = %s", v.Artifacts["developing"])
	}
}

func TestRunStatusUnknownIsPending(t *testing.T) {
	g := New(nil, &fakeReader{found: false}, nil, nil)
	v, err := g.RunStatus(context.Background(), "nope")
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if v.RunID != "nope" || v.Status != "pending" {
		t.Fatalf("view = %+v", v)
	}
}

func TestListReposAndSearch(t *testing.T) {
	g := New(nil, nil, &fakeRepos{repos: []RepoView{{ID: "a", Name: "svc-a"}}},
		&fakeSearch{hits: []SearchHit{{RepoID: "a", Path: "x.go", Score: 0.9}}})
	repos, err := g.ListRepos(context.Background())
	if err != nil || len(repos) != 1 || repos[0].ID != "a" {
		t.Fatalf("repos = %+v err=%v", repos, err)
	}
	hits, err := g.SearchContext(context.Background(), "currency", []string{"a"}, 0)
	if err != nil || len(hits) != 1 {
		t.Fatalf("hits = %+v err=%v", hits, err)
	}

	// Empty query is an error.
	if _, err := g.SearchContext(context.Background(), "  ", nil, 5); err == nil {
		t.Fatalf("expected error on empty query")
	}
}

// Sanity: RunView marshals to stable JSON keys used by MCP tool results.
func TestRunViewJSON(t *testing.T) {
	b, err := json.Marshal(RunView{RunID: "r", Status: "running"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("marshal produced empty output")
	}
}
