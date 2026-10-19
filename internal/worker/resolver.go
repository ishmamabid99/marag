package worker

import (
	"context"
	"fmt"
)

// RepoCheckout tells the developer where to clone a repo from and what to base on.
type RepoCheckout struct {
	CloneURL   string // CLEAN clonable location (username-only https in prod; local path in tests) — no embedded secret
	Token      string // optional credential supplied out-of-band to git (never persisted in the workspace)
	BaseBranch string // default branch to base the work branch on (informational for now)
}

// RepoResolver resolves a repo id to a clonable checkout. The real impl (in the
// git-publisher wiring) builds an authenticated URL from the registry; tests use FakeResolver.
type RepoResolver interface {
	Resolve(ctx context.Context, repoID string) (RepoCheckout, error)
}

// FakeResolver is an in-memory RepoResolver for tests.
type FakeResolver struct {
	Checkouts map[string]RepoCheckout
	Err       error
}

// Resolve returns the mapped checkout, FakeResolver.Err, or an unknown-repo error.
func (f *FakeResolver) Resolve(_ context.Context, repoID string) (RepoCheckout, error) {
	if f.Err != nil {
		return RepoCheckout{}, f.Err
	}
	co, ok := f.Checkouts[repoID]
	if !ok {
		return RepoCheckout{}, fmt.Errorf("fake resolver: unknown repo %q", repoID)
	}
	return co, nil
}
