package publisher

import (
	"context"
	"testing"

	"github.com/fin/multi-agent-rag/internal/gitprovider"
)

func TestCompensationExecutorClosePRAndDeleteBranch(t *testing.T) {
	prov := gitprovider.NewFake()
	c := NewCompensationExecutor(idRepos{}, func(string) (gitprovider.GitProvider, error) { return prov, nil }, func(string) string { return "tok" })
	ctx := context.Background()

	if err := c.Execute(ctx, "close_pr", `{"repo_id":"repo-a","pr_id":"42"}`); err != nil {
		t.Fatalf("close_pr: %v", err)
	}
	if len(prov.Closed) != 1 || prov.Closed[0] != "42" {
		t.Fatalf("closed = %+v", prov.Closed)
	}
	if err := c.Execute(ctx, "delete_branch", `{"repo_id":"repo-a","branch":"marag/run-1"}`); err != nil {
		t.Fatalf("delete_branch: %v", err)
	}
	if len(prov.DeletedBranches) != 1 || prov.DeletedBranches[0] != "marag/run-1" {
		t.Fatalf("deleted = %+v", prov.DeletedBranches)
	}
}

func TestCompensationExecutorUnknownAction(t *testing.T) {
	c := NewCompensationExecutor(idRepos{}, func(string) (gitprovider.GitProvider, error) { return gitprovider.NewFake(), nil }, func(string) string { return "t" })
	if err := c.Execute(context.Background(), "explode", `{"repo_id":"r"}`); err == nil {
		t.Fatalf("expected error for unknown action")
	}
}

func TestCompensationExecutorMissingParams(t *testing.T) {
	c := NewCompensationExecutor(idRepos{}, func(string) (gitprovider.GitProvider, error) { return gitprovider.NewFake(), nil }, func(string) string { return "t" })
	// close_pr without pr_id → error (nothing to close).
	if err := c.Execute(context.Background(), "close_pr", `{"repo_id":"repo-a"}`); err == nil {
		t.Fatalf("expected error for missing pr_id")
	}
}
