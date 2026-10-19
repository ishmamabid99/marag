package gitprovider

import (
	"context"
	"testing"
)

func TestFakeRecordsCreateAndClose(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	repo := RepoRef{Owner: "o", Name: "r"}

	res, err := f.CreatePR(ctx, repo, PRRequest{Title: "t", SourceBranch: "feat/x", TargetBranch: "main"})
	if err != nil {
		t.Fatalf("CreatePR: %v", err)
	}
	if res.URL == "" || res.ID == "" {
		t.Fatalf("result = %+v", res)
	}
	if len(f.Created) != 1 || f.Created[0].SourceBranch != "feat/x" {
		t.Fatalf("created = %+v", f.Created)
	}
	if err := f.ClosePR(ctx, repo, res.ID); err != nil {
		t.Fatalf("ClosePR: %v", err)
	}
	if len(f.Closed) != 1 || f.Closed[0] != res.ID {
		t.Fatalf("closed = %+v", f.Closed)
	}
	if err := f.DeleteBranch(ctx, repo, "feat/x"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	if len(f.DeletedBranches) != 1 {
		t.Fatalf("deleted = %+v", f.DeletedBranches)
	}
	db, _ := f.GetDefaultBranch(ctx, repo)
	if db != "main" {
		t.Fatalf("default branch = %q", db)
	}
}
