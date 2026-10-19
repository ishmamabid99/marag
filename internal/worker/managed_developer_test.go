package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/fin/multi-agent-rag/internal/managedagent"
)

func maResolver(repos map[string]string) *FakeResolver {
	c := map[string]RepoCheckout{}
	for id, url := range repos {
		c[id] = RepoCheckout{CloneURL: url, BaseBranch: "main"}
	}
	return &FakeResolver{Checkouts: c}
}

func TestManagedDeveloperMultiRepoBestEffort(t *testing.T) {
	client := managedagent.NewFakeMAClient(func(_ context.Context, req managedagent.MASession) (managedagent.MAResult, error) {
		if req.RepoID == "repo-b" {
			return managedagent.MAResult{TestsPassed: false, Summary: "could not fix"}, nil
		}
		return managedagent.MAResult{Branch: req.Branch, HeadCommit: "sha-" + req.RepoID, FilesChanged: []string{"x.go"}, TestsPassed: true, Summary: "impl " + req.RepoID}, nil
	})
	dev := NewManagedDeveloper(client, maResolver(map[string]string{"repo-a": "https://git/repo-a", "repo-b": "https://git/repo-b"}), "claude-sonnet-5")
	if dev.Role() != "developer" {
		t.Fatalf("role = %q", dev.Role())
	}
	res, err := dev.Handle(context.Background(), StageInput{RunID: "run-1", Stage: "developing", Spec: "feature", RepoIDs: []string{"repo-a", "repo-b"}})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var art DeveloperArtifact
	if err := json.Unmarshal(res.ArtifactJSON, &art); err != nil {
		t.Fatalf("artifact: %v", err)
	}
	if len(art.Repos) != 2 {
		t.Fatalf("repos = %+v", art.Repos)
	}
	// repo-a succeeded (primary), repo-b failed best-effort.
	if art.RepoID != "repo-a" || !art.TestsPassed || art.BranchName != "marag/run-1" {
		t.Fatalf("primary = %+v", art)
	}
	var b RepoResult
	for _, r := range art.Repos {
		if r.RepoID == "repo-b" {
			b = r
		}
	}
	if b.TestsPassed || b.Failure == "" {
		t.Fatalf("repo-b should be failed with reason: %+v", b)
	}
	// The MA session got the plain remote URL + branch.
	if c := client.Calls(); len(c) != 2 || c[0].RemoteURL != "https://git/repo-a" || c[0].Branch != "marag/run-1" {
		t.Fatalf("sessions = %+v", client.Calls())
	}
}

func TestManagedDeveloperAllFail(t *testing.T) {
	client := managedagent.NewFakeMAClient(func(_ context.Context, _ managedagent.MASession) (managedagent.MAResult, error) {
		return managedagent.MAResult{TestsPassed: false}, nil
	})
	dev := NewManagedDeveloper(client, maResolver(map[string]string{"repo-a": "u"}), "claude-sonnet-5")
	_, err := dev.Handle(context.Background(), StageInput{RunID: "run-2", Stage: "developing", Spec: "x", RepoIDs: []string{"repo-a"}})
	if !errors.Is(err, ErrDevExhausted) {
		t.Fatalf("err = %v, want ErrDevExhausted", err)
	}
}

func TestManagedDeveloperNoRepoOrRunID(t *testing.T) {
	client := managedagent.NewFakeMAClient(func(_ context.Context, _ managedagent.MASession) (managedagent.MAResult, error) {
		return managedagent.MAResult{TestsPassed: true}, nil
	})
	dev := NewManagedDeveloper(client, maResolver(map[string]string{"repo-a": "u"}), "m")
	if _, err := dev.Handle(context.Background(), StageInput{RunID: "run", Stage: "developing", RepoIDs: nil}); err == nil {
		t.Fatalf("expected error for no repo")
	}
	if _, err := dev.Handle(context.Background(), StageInput{RunID: "", Stage: "developing", RepoIDs: []string{"repo-a"}}); err == nil {
		t.Fatalf("expected error for empty run id")
	}
}
