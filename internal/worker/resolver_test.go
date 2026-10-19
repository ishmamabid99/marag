package worker

import (
	"context"
	"testing"
)

func TestFakeResolver(t *testing.T) {
	r := &FakeResolver{Checkouts: map[string]RepoCheckout{"repo-a": {CloneURL: "/tmp/bare", BaseBranch: "main"}}}
	co, err := r.Resolve(context.Background(), "repo-a")
	if err != nil || co.CloneURL != "/tmp/bare" || co.BaseBranch != "main" {
		t.Fatalf("resolve = %+v err=%v", co, err)
	}
	if _, err := r.Resolve(context.Background(), "missing"); err == nil {
		t.Fatalf("expected error for unknown repo")
	}
}
