// Package gitprovider abstracts git-hosting PR/MR operations behind a
// provider-neutral interface with GitHub + GitLab backends. Dependency-free.
package gitprovider

import "context"

// RepoRef identifies a repo on a hosting provider, with API base + token.
type RepoRef struct {
	Owner      string
	Name       string
	APIBaseURL string // overrides the provider default (GHE / self-hosted / tests)
	Token      string
}

// PRRequest is a provider-neutral pull/merge request.
type PRRequest struct {
	Title        string
	Body         string
	SourceBranch string
	TargetBranch string
}

// PRResult is the created PR/MR.
type PRResult struct {
	ID     string // provider id used for later close (PR number / MR iid, as string)
	Number int
	URL    string
}

// GitProvider performs provider-neutral git-hosting operations.
type GitProvider interface {
	GetDefaultBranch(ctx context.Context, repo RepoRef) (string, error)
	CreatePR(ctx context.Context, repo RepoRef, pr PRRequest) (PRResult, error)
	ClosePR(ctx context.Context, repo RepoRef, prID string) error
	DeleteBranch(ctx context.Context, repo RepoRef, branch string) error
}

// Compile-time assertions that every backend satisfies GitProvider.
var (
	_ GitProvider = (*Fake)(nil)
	_ GitProvider = (*GitHub)(nil)
	_ GitProvider = (*GitLab)(nil)
)
