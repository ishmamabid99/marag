package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fin/multi-agent-rag/internal/managedagent"
)

// ManagedDeveloper runs the developer stage via Managed Agents (one MA session
// per repo), emitting the same DeveloperArtifact as the local Developer.
type ManagedDeveloper struct {
	client   managedagent.MAClient
	resolver RepoResolver
	model    string
}

// NewManagedDeveloper wires the MA-backed developer.
func NewManagedDeveloper(client managedagent.MAClient, resolver RepoResolver, model string) *ManagedDeveloper {
	return &ManagedDeveloper{client: client, resolver: resolver, model: model}
}

func (h *ManagedDeveloper) Role() string { return "developer" }

// Handle runs an MA session per repo (best-effort) and returns a DeveloperArtifact.
func (h *ManagedDeveloper) Handle(ctx context.Context, in StageInput) (StageResult, error) {
	if len(in.RepoIDs) == 0 {
		return StageResult{}, fmt.Errorf("managed-developer: no repo id in stage input")
	}
	if in.RunID == "" {
		return StageResult{}, fmt.Errorf("managed-developer: empty run id")
	}
	branch := "marag/" + in.RunID

	var results []RepoResult
	for _, repoID := range in.RepoIDs {
		results = append(results, h.runRepo(ctx, in, repoID, branch))
	}

	var primary *RepoResult
	for i := range results {
		if results[i].TestsPassed {
			primary = &results[i]
			break
		}
	}
	if primary == nil {
		return StageResult{}, fmt.Errorf("%w (all %d repos failed via MA)", ErrDevExhausted, len(results))
	}

	art := DeveloperArtifact{
		RepoID:       primary.RepoID,
		BranchName:   primary.BranchName,
		HeadCommit:   primary.HeadCommit,
		FilesChanged: primary.FilesChanged,
		TestsPassed:  true,
		Repos:        results,
		Summary:      primary.summary,
	}
	out, err := json.Marshal(art)
	if err != nil {
		return StageResult{}, fmt.Errorf("managed-developer: marshal artifact: %w", err)
	}
	return StageResult{Stage: in.Stage, ArtifactJSON: out}, nil
}

// runRepo runs one repo's MA session, best-effort (failures captured, never errors).
func (h *ManagedDeveloper) runRepo(ctx context.Context, in StageInput, repoID, branch string) RepoResult {
	res := RepoResult{RepoID: repoID}
	co, err := h.resolver.Resolve(ctx, repoID)
	if err != nil {
		res.Failure = fmt.Sprintf("resolve: %v", err)
		return res
	}
	out, err := h.client.RunDeveloperSession(ctx, managedagent.MASession{
		Spec: in.Spec, RepoID: repoID, RemoteURL: co.CloneURL, BaseBranch: co.BaseBranch,
		Branch: branch, Model: h.model,
	})
	if err != nil {
		res.Failure = fmt.Sprintf("ma session: %v", err)
		return res
	}
	if !out.TestsPassed {
		res.Failure = "MA session did not reach passing tests"
		res.summary = out.Summary
		return res
	}
	res.BranchName = branch
	if out.Branch != "" {
		res.BranchName = out.Branch
	}
	res.HeadCommit = out.HeadCommit
	res.FilesChanged = out.FilesChanged
	res.TestsPassed = true
	res.summary = out.Summary
	return res
}
