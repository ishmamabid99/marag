package publisher

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fin/multi-agent-rag/internal/gitprovider"
)

// CompensationExecutor rolls back a run's git side effects (close PR, delete branch).
type CompensationExecutor struct {
	repos    RepoInfo
	provider func(name string) (gitprovider.GitProvider, error)
	tokenFor func(provider string) string
}

// NewCompensationExecutor wires the executor.
func NewCompensationExecutor(repos RepoInfo, provider func(name string) (gitprovider.GitProvider, error), tokenFor func(provider string) string) *CompensationExecutor {
	return &CompensationExecutor{repos: repos, provider: provider, tokenFor: tokenFor}
}

// Execute performs one compensation action given its JSON params.
func (c *CompensationExecutor) Execute(ctx context.Context, action, paramsJSON string) error {
	var p struct {
		RepoID string `json:"repo_id"`
		PRID   string `json:"pr_id"`
		Branch string `json:"branch"`
	}
	if err := json.Unmarshal([]byte(paramsJSON), &p); err != nil {
		return fmt.Errorf("publisher: compensation params: %w", err)
	}
	if p.RepoID == "" {
		return fmt.Errorf("publisher: compensation missing repo_id")
	}
	meta, err := c.repos.Get(ctx, p.RepoID)
	if err != nil {
		return fmt.Errorf("publisher: compensation repo meta %q: %w", p.RepoID, err)
	}
	owner, name, err := parseOwnerName(meta.RemoteURL)
	if err != nil {
		return err
	}
	gp, err := c.provider(meta.Provider)
	if err != nil {
		return fmt.Errorf("publisher: compensation provider %q: %w", meta.Provider, err)
	}
	repo := gitprovider.RepoRef{Owner: owner, Name: name, Token: c.tokenFor(meta.Provider)}
	switch action {
	case "close_pr":
		if p.PRID == "" {
			return fmt.Errorf("publisher: close_pr missing pr_id")
		}
		if err := gp.ClosePR(ctx, repo, p.PRID); err != nil {
			return fmt.Errorf("publisher: close_pr: %w", err)
		}
	case "delete_branch":
		if p.Branch == "" {
			return fmt.Errorf("publisher: delete_branch missing branch")
		}
		if err := gp.DeleteBranch(ctx, repo, p.Branch); err != nil {
			return fmt.Errorf("publisher: delete_branch: %w", err)
		}
	default:
		return fmt.Errorf("publisher: unknown compensation action %q", action)
	}
	return nil
}
