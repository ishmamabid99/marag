// Package publisher opens a PR/MR from a developer's pushed branch and registers
// compensations. Provider-agnostic via internal/gitprovider.
package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fin/multi-agent-rag/internal/events"
	"github.com/fin/multi-agent-rag/internal/gitprovider"
	"github.com/fin/multi-agent-rag/internal/kafka"
)

// RepoMeta is the hosting metadata the publisher needs.
type RepoMeta struct {
	Provider      string
	RemoteURL     string
	DefaultBranch string
}

// RepoInfo resolves a repo id to hosting metadata (adapter over the registry).
type RepoInfo interface {
	Get(ctx context.Context, repoID string) (RepoMeta, error)
}

// Comp is a compensation action to register.
type Comp struct {
	Action     string
	ParamsJSON string
}

// Compensator records compensations (adapter over the orchestrator store).
type Compensator interface {
	Register(ctx context.Context, runID string, comps []Comp) error
}

// Producer publishes events.
type Producer interface {
	Publish(ctx context.Context, topic, key string, value []byte) error
}

// Claimer records a first-writer-wins key (true = newly claimed) and can
// release it (so a fully-failed delivery can be retried on replay).
type Claimer interface {
	Claim(ctx context.Context, key string) (bool, error)
	Release(ctx context.Context, key string) error
}

// DeliverRepo is one repo's pushed branch to open a PR for.
type DeliverRepo struct {
	RepoID     string `json:"repo_id"`
	Branch     string `json:"branch"`
	HeadCommit string `json:"head_commit"`
}

// DeliverEvent is the decoded deliver.pr payload.
type DeliverEvent struct {
	RunID string        `json:"-"`
	Repos []DeliverRepo `json:"repos"`
	Title string        `json:"title"`
	Body  string        `json:"body"`
}

// OpenedPR is a PR the publisher opened.
type OpenedPR struct {
	RepoID string `json:"repo_id"`
	PRID   string `json:"pr_id"`
	URL    string `json:"pr_url"`
}

// Publisher opens PRs and registers compensations.
type Publisher struct {
	repos    RepoInfo
	provider func(name string) (gitprovider.GitProvider, error)
	tokenFor func(provider string) string
	comp     Compensator
	prod     Producer
	claimer  Claimer
}

// New wires the publisher.
func New(repos RepoInfo, provider func(name string) (gitprovider.GitProvider, error), tokenFor func(provider string) string, comp Compensator, prod Producer) *Publisher {
	return &Publisher{repos: repos, provider: provider, tokenFor: tokenFor, comp: comp, prod: prod}
}

// WithClaimer enables delivery dedup: a run whose key is already claimed is skipped.
func (p *Publisher) WithClaimer(c Claimer) *Publisher { p.claimer = c; return p }

// Deliver opens one PR per repo (best-effort) and registers compensations.
// A per-repo failure is recorded and the loop continues; Deliver returns an
// error only if every repo failed to deliver.
func (p *Publisher) Deliver(ctx context.Context, evt DeliverEvent) ([]OpenedPR, error) {
	if len(evt.Repos) == 0 {
		return nil, fmt.Errorf("publisher: no repos to deliver")
	}
	if p.claimer != nil {
		claimed, err := p.claimer.Claim(ctx, "deliver:"+evt.RunID)
		if err != nil {
			// best-effort: proceed rather than drop a delivery (dup PR > no PR).
			// (log at call site)
		} else if !claimed {
			return nil, nil // already delivered for this run — skip
		}
	}
	siblings := make([]string, 0, len(evt.Repos))
	for _, r := range evt.Repos {
		siblings = append(siblings, r.RepoID)
	}

	var (
		opened []OpenedPR
		failed []map[string]string
	)
	for _, dr := range evt.Repos {
		res, err := p.openOne(ctx, evt, dr, siblings)
		if err != nil {
			failed = append(failed, map[string]string{"repo_id": dr.RepoID, "error": err.Error()})
			continue
		}
		opened = append(opened, res)
	}

	if err := p.emitOpened(ctx, evt.RunID, opened, failed); err != nil {
		return opened, err
	}
	if len(opened) == 0 {
		// Every repo failed: release the claim so a replay/DLQ-reprocess can
		// retry (otherwise the claim is burned and the run strands forever).
		if p.claimer != nil {
			_ = p.claimer.Release(ctx, "deliver:"+evt.RunID)
		}
		return nil, fmt.Errorf("publisher: all %d repos failed to deliver", len(evt.Repos))
	}
	return opened, nil
}

// openOne opens a single PR for one repo and registers its compensations.
func (p *Publisher) openOne(ctx context.Context, evt DeliverEvent, dr DeliverRepo, siblings []string) (OpenedPR, error) {
	if dr.Branch == "" {
		return OpenedPR{}, fmt.Errorf("publisher: empty branch for repo %s — nothing to open a PR from", dr.RepoID)
	}
	meta, err := p.repos.Get(ctx, dr.RepoID)
	if err != nil {
		return OpenedPR{}, fmt.Errorf("publisher: repo meta %q: %w", dr.RepoID, err)
	}
	owner, name, err := parseOwnerName(meta.RemoteURL)
	if err != nil {
		return OpenedPR{}, err
	}
	gp, err := p.provider(meta.Provider)
	if err != nil {
		return OpenedPR{}, fmt.Errorf("publisher: provider %q: %w", meta.Provider, err)
	}
	repo := gitprovider.RepoRef{Owner: owner, Name: name, Token: p.tokenFor(meta.Provider)}
	target := meta.DefaultBranch
	if target == "" {
		if target, err = gp.GetDefaultBranch(ctx, repo); err != nil {
			return OpenedPR{}, fmt.Errorf("publisher: default branch: %w", err)
		}
	}
	body := evt.Body
	if len(siblings) > 1 {
		body += "\n\nPart of a multi-repo change across: " + strings.Join(siblings, ", ")
	}
	res, err := gp.CreatePR(ctx, repo, gitprovider.PRRequest{
		Title: evt.Title, Body: body, SourceBranch: dr.Branch, TargetBranch: target,
	})
	if err != nil {
		return OpenedPR{}, fmt.Errorf("publisher: create PR: %w", err)
	}

	// Registered delete-then-close so reverse-order execution closes the PR
	// before deleting its branch.
	delParams, _ := json.Marshal(map[string]string{"branch": dr.Branch, "repo_id": dr.RepoID})
	closeParams, _ := json.Marshal(map[string]string{"pr_id": res.ID, "repo_id": dr.RepoID})
	if err := p.comp.Register(ctx, evt.RunID, []Comp{
		{Action: "delete_branch", ParamsJSON: string(delParams)},
		{Action: "close_pr", ParamsJSON: string(closeParams)},
	}); err != nil {
		// The PR was created but has no registered compensation — best-effort
		// close it now so we don't leak an open PR with no rollback path.
		_ = gp.ClosePR(ctx, repo, res.ID)
		return OpenedPR{}, fmt.Errorf("publisher: register compensations (closed orphan PR %s): %w", res.ID, err)
	}

	return OpenedPR{RepoID: dr.RepoID, PRID: res.ID, URL: res.URL}, nil
}

func (p *Publisher) emitOpened(ctx context.Context, runID string, opened []OpenedPR, failed []map[string]string) error {
	env, err := events.NewEnvelope(runID, "run.pr.opened", "git-publisher", 1)
	if err != nil {
		return fmt.Errorf("publisher: envelope: %w", err)
	}
	body, err := json.Marshal(map[string]any{"prs": opened, "failed": failed})
	if err != nil {
		return fmt.Errorf("publisher: marshal opened: %w", err)
	}
	value, err := events.Marshal(&env, body)
	if err != nil {
		return fmt.Errorf("publisher: marshal envelope: %w", err)
	}
	if err := p.prod.Publish(ctx, kafka.TopicRunPROpened, runID, value); err != nil {
		return fmt.Errorf("publisher: publish opened: %w", err)
	}
	return nil
}
