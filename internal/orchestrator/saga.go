package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/fin/multi-agent-rag/internal/events"
	"github.com/fin/multi-agent-rag/internal/kafka"
)

// Producer publishes engine output events (satisfied by *kafka.Producer).
type Producer interface {
	Publish(ctx context.Context, topic, key string, value []byte) error
}

// CompensationExecutor performs a registered compensation's git side effect.
type CompensationExecutor interface {
	Execute(ctx context.Context, action, paramsJSON string) error
}

// Engine drives run state transitions and emits the next command with
// produce-after-commit semantics.
type Engine struct {
	store    *Store
	prod     Producer
	executor CompensationExecutor
}

// NewEngine wires the saga engine.
func NewEngine(store *Store, prod Producer) *Engine {
	return &Engine{store: store, prod: prod}
}

// WithCompensationExecutor sets the executor used to roll back git side
// effects (close PR, delete branch) on terminal failure. A nil executor
// (the default) keeps the mark-only behavior.
func (e *Engine) WithCompensationExecutor(x CompensationExecutor) *Engine {
	e.executor = x
	return e
}

// StageCompleted is the input for a completed agent stage. ConfirmedComplexity
// is honored only when Stage == StageViability; GateRecommended only when
// Stage == StageRisk; QAPassed only when Stage == StageQA.
type StageCompleted struct {
	RunID               string
	Stage               Stage
	ConfirmedComplexity Complexity
	GateRecommended     bool
	QAPassed            bool
	Artifact            []byte
	// ProposedRepos (analyzer only) names repos outside the run's candidate set
	// the feature also appears to need — surfaced as a human gate, not acted on.
	ProposedRepos []string
	// GateReason (risk only) is the risk-analyzer's rationale for recommending a
	// human gate; forwarded into the gate event so it can be surfaced downstream.
	GateReason string
}

// StartRun creates a run and emits the first stage request. Idempotent: if the
// run already exists, it re-emits the analyzing request without re-creating.
func (e *Engine) StartRun(ctx context.Context, runID, spec string, repoIDs []string) error {
	existing, err := e.store.GetRun(ctx, runID)
	switch {
	case err == nil:
		// Already created — recover by re-emitting the first request.
		return e.emitStage(ctx, existing, FirstStage)
	case !isNotFound(err):
		return fmt.Errorf("start run: %w", err)
	}
	run := Run{
		ID: runID, Status: StatusRunning, Complexity: ComplexityComplex,
		CurrentStage: FirstStage, Spec: spec, RepoIDs: repoIDs,
	}
	if err := e.store.CreateRun(ctx, run); err != nil {
		if errors.Is(err, ErrRunExists) {
			return e.emitStage(ctx, run, FirstStage)
		}
		return fmt.Errorf("start run: %w", err)
	}
	return e.emitStage(ctx, run, FirstStage)
}

// OnStageCompleted advances the run past a completed stage.
func (e *Engine) OnStageCompleted(ctx context.Context, evt StageCompleted) error {
	run, err := e.store.GetRun(ctx, evt.RunID)
	if err != nil {
		return fmt.Errorf("on stage completed: %w", err)
	}
	if run.Status != StatusRunning {
		// A run that is delivering/completed/failed must not be advanced by a
		// (stale/duplicate) completion.
		return nil
	}
	// State-derived idempotency: if the completed stage isn't the one in flight,
	// this is a duplicate/replay — re-emit the current command and return.
	if run.CurrentStage != evt.Stage {
		return e.emitStage(ctx, run, run.CurrentStage)
	}

	if len(evt.Artifact) > 0 {
		if err := e.store.SaveArtifact(ctx, run.ID, evt.Stage, evt.Artifact); err != nil {
			return fmt.Errorf("on stage completed: %w", err)
		}
	}

	complexity := run.Complexity
	if evt.Stage == StageViability && evt.ConfirmedComplexity.Valid() {
		if err := e.store.SetComplexity(ctx, run.ID, evt.ConfirmedComplexity); err != nil {
			return fmt.Errorf("on stage completed: %w", err)
		}
		complexity = evt.ConfirmedComplexity
	}

	// Analyzer may propose repos outside the candidate set. Propose, don't act:
	// park at a human gate; do not expand scope until approved.
	if evt.Stage == StageAnalyzing {
		if add := notIn(run.RepoIDs, evt.ProposedRepos); len(add) > 0 {
			reason := "proposed additional repos: " + strings.Join(add, ", ") + " — approve to include"
			if err := e.store.GateRun(ctx, run.ID, StageAnalyzing, reason); err != nil {
				return fmt.Errorf("on stage completed: %w", err)
			}
			return e.emitGate(ctx, run, StageAnalyzing, reason)
		}
	}

	// Risk stage may recommend a human gate: park the run, notify, do not advance.
	if evt.Stage == StageRisk && evt.GateRecommended {
		reason := evt.GateReason
		if reason == "" {
			reason = "risk-analyzer recommended a human gate"
		}
		if err := e.store.GateRun(ctx, run.ID, StageRisk, reason); err != nil {
			return fmt.Errorf("on stage completed: %w", err)
		}
		return e.emitGate(ctx, run, StageRisk, reason)
	}

	// QA stage drives the developer⇄QA loop.
	if evt.Stage == StageQA {
		if evt.QAPassed {
			if err := e.store.AdvanceRun(ctx, run.ID, StageQA, StatusDelivering, "qa passed; delivering"); err != nil {
				return fmt.Errorf("on stage completed: %w", err)
			}
			return e.emitDeliver(ctx, run.ID)
		}
		// Increment + route in ONE transaction (M1): the counter and the stage
		// advance can never diverge, so a redelivery can't double-count.
		iters, gated, err := e.store.IncrementQAAndRoute(ctx, run.ID, MaxQAIterations)
		if err != nil {
			return fmt.Errorf("on stage completed: %w", err)
		}
		if gated {
			return e.emitGate(ctx, run, StageQA, fmt.Sprintf("qa failed after %d iterations", iters))
		}
		run.QAIterations = iters // reflect the increment so emitStage stamps attempt N+1
		return e.emitStage(ctx, run, StageDeveloping)
	}

	next, hasNext, err := NextStage(evt.Stage, complexity)
	if err != nil {
		return fmt.Errorf("on stage completed: %w", err)
	}
	if !hasNext {
		// qa done → deliver.
		if err := e.store.AdvanceRun(ctx, run.ID, evt.Stage, StatusDelivering, "qa complete; delivering"); err != nil {
			return fmt.Errorf("on stage completed: %w", err)
		}
		return e.emitDeliver(ctx, run.ID)
	}
	// Persist the advance (commit), then produce (produce-after-commit).
	if err := e.store.AdvanceRun(ctx, run.ID, next, StatusRunning, "advance to "+string(next)); err != nil {
		return fmt.Errorf("on stage completed: %w", err)
	}
	return e.emitStage(ctx, run, next)
}

// PRRef identifies one pull/merge request the git-publisher opened.
type PRRef struct {
	RepoID string
	PRID   string
	URL    string
}

// PROpened is the input for a delivered run. Opened lists the PRs that were
// successfully opened; a non-empty Opened means delivery succeeded.
type PROpened struct {
	RunID  string
	Opened []PRRef
}

// OnPROpened finalizes a delivering run once its PR(s) are opened, marking it
// completed — the transition that moves a run out of the non-terminal
// `delivering` state. Idempotent: a no-op for a run that is not currently
// delivering (already completed on a duplicate event, or never reached
// delivery), and for an empty Opened set. An empty set means every repo failed
// to deliver; the git-publisher returns an error and retries/DLQs that case, so
// the run is intentionally left `delivering` rather than completed here.
func (e *Engine) OnPROpened(ctx context.Context, evt PROpened) error {
	run, err := e.store.GetRun(ctx, evt.RunID)
	if err != nil {
		return fmt.Errorf("on pr opened: %w", err)
	}
	if run.Status != StatusDelivering {
		return nil
	}
	if len(evt.Opened) == 0 {
		return nil
	}
	note := "pr opened; completed"
	if u := evt.Opened[0].URL; u != "" {
		if len(evt.Opened) == 1 {
			note = fmt.Sprintf("pr opened; completed (%s)", u)
		} else {
			note = fmt.Sprintf("pr opened; completed (%d PRs, first %s)", len(evt.Opened), u)
		}
	}
	// Keep the run parked at its current stage (qa); only the status advances.
	if err := e.store.AdvanceRun(ctx, run.ID, run.CurrentStage, StatusCompleted, note); err != nil {
		return fmt.Errorf("on pr opened: %w", err)
	}
	return nil
}

// OnStageFailed marks the run failed and runs compensations in reverse order.
func (e *Engine) OnStageFailed(ctx context.Context, runID string, stage Stage, reason string) error {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("on stage failed: %w", err)
	}
	if run.Status.isTerminal() {
		// Already failed/completed — ignore a stale/duplicate failure.
		return nil
	}
	return e.failAndCompensate(ctx, runID, stage, reason)
}

// failAndCompensate marks the run failed and best-effort executes pending
// compensations in reverse registration order, keeping the first error.
func (e *Engine) failAndCompensate(ctx context.Context, runID string, stage Stage, reason string) error {
	if err := e.store.FailRun(ctx, runID, stage, reason); err != nil {
		return fmt.Errorf("fail and compensate: %w", err)
	}
	pending, err := e.store.PendingCompensations(ctx, runID)
	if err != nil {
		return fmt.Errorf("fail and compensate: %w", err)
	}
	var firstErr error
	for _, c := range pending {
		if e.executor != nil {
			if err := e.executor.Execute(ctx, c.Action, c.ParamsJSON); err != nil {
				// Leave the compensation PENDING (M2): marking a failed rollback as
				// executed permanently orphans the PR/branch. A future reconcile
				// sweep can retry a still-pending compensation.
				log.Printf("orchestrator: compensation %s for run %s failed (left pending): %v", c.Action, runID, err)
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
		}
		if err := e.store.MarkCompensationExecuted(ctx, c.ID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ResumeGate continues a run parked at a human gate. approved=true resumes the
// pipeline (delivering if parked at qa, else advancing to the next stage);
// approved=false fails the run and runs compensations. It is a no-op for a run
// that is not currently gated (idempotent against duplicate human actions).
func (e *Engine) ResumeGate(ctx context.Context, runID string, approved bool) error {
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("resume gate: %w", err)
	}
	if run.Status != StatusGatedWaitingHuman {
		return nil
	}
	// A gate parked at analyzing is a repo-scope PROPOSAL, not a risk stop:
	// approve expands the scope with the proposed repos; reject proceeds with
	// the original repos. Neither fails the run.
	if run.CurrentStage == StageAnalyzing {
		if approved {
			if err := e.expandFromAnalyzerProposal(ctx, &run); err != nil {
				return fmt.Errorf("resume gate: %w", err)
			}
		}
		return e.resumeAfterAnalyzerGate(ctx, run)
	}
	if !approved {
		return e.failAndCompensate(ctx, runID, run.CurrentStage, "human rejected gate")
	}
	// Approved: a gate parked at qa means "ship despite QA"; otherwise continue.
	if run.CurrentStage == StageQA {
		if err := e.store.AdvanceRun(ctx, run.ID, StageQA, StatusDelivering, "gate approved; delivering"); err != nil {
			return fmt.Errorf("resume gate: %w", err)
		}
		return e.emitDeliver(ctx, run.ID)
	}
	next, hasNext, err := NextStage(run.CurrentStage, run.Complexity)
	if err != nil {
		return fmt.Errorf("resume gate: %w", err)
	}
	if !hasNext {
		if err := e.store.AdvanceRun(ctx, run.ID, run.CurrentStage, StatusDelivering, "gate approved; delivering"); err != nil {
			return fmt.Errorf("resume gate: %w", err)
		}
		return e.emitDeliver(ctx, run.ID)
	}
	if err := e.store.AdvanceRun(ctx, run.ID, next, StatusRunning, "gate approved; advance to "+string(next)); err != nil {
		return fmt.Errorf("resume gate: %w", err)
	}
	return e.emitStage(ctx, run, next)
}

func (e *Engine) emitStage(ctx context.Context, run Run, stage Stage) error {
	env, err := events.NewEnvelope(run.ID, "stage.requested", "orchestrator", 1)
	if err != nil {
		return fmt.Errorf("emit stage: %w", err)
	}
	// Override the idempotency key to include the stage: NewEnvelope's default
	// key is identical for every stage of a run, so without this a downstream
	// worker deduping on the key would drop later stages as duplicates. A
	// recovery re-emit of the same stage still dedupes correctly.
	env.IdempotencyKey = fmt.Sprintf("%s:stage.requested:%s", run.ID, string(stage))
	arts, err := e.store.LoadArtifacts(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("emit stage: load artifacts: %w", err)
	}
	prior := make(map[string]json.RawMessage, len(arts))
	for st, a := range arts {
		prior[string(st)] = json.RawMessage(a)
	}
	payload, err := json.Marshal(map[string]any{
		"role":            RoleForStage(stage),
		"stage":           string(stage),
		"spec":            run.Spec,
		"repo_ids":        run.RepoIDs,
		"prior_artifacts": prior,
		"attempt":         run.QAIterations + 1,
	})
	if err != nil {
		return fmt.Errorf("emit stage: marshal: %w", err)
	}
	value, err := events.Marshal(&env, payload)
	if err != nil {
		return fmt.Errorf("emit stage: %w", err)
	}
	if err := e.prod.Publish(ctx, kafka.TopicStageRequested, run.ID, value); err != nil {
		return fmt.Errorf("emit stage publish: %w", err)
	}
	return nil
}

func (e *Engine) emitDeliver(ctx context.Context, runID string) error {
	env, err := events.NewEnvelope(runID, "deliver.pr", "orchestrator", 1)
	if err != nil {
		return fmt.Errorf("emit deliver: %w", err)
	}
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("emit deliver: %w", err)
	}
	var branch, headCommit, summary string
	deliverRepoIDs := run.RepoIDs
	arts, err := e.store.LoadArtifacts(ctx, runID)
	if err != nil {
		return fmt.Errorf("emit deliver: %w", err)
	}
	var da struct {
		RepoID     string `json:"repo_id"`
		BranchName string `json:"branch_name"`
		HeadCommit string `json:"head_commit"`
		Summary    string `json:"summary"`
		QANote     string `json:"qa_note"`
		Repos      []struct {
			RepoID          string `json:"repo_id"`
			BranchName      string `json:"branch_name"`
			HeadCommit      string `json:"head_commit"`
			TestsPassed     bool   `json:"tests_passed"`
			TestsUnverified bool   `json:"tests_unverified"`
		} `json:"repos"`
	}
	if dev, ok := arts[StageDeveloping]; ok {
		_ = json.Unmarshal(dev, &da)
		branch, headCommit, summary = da.BranchName, da.HeadCommit, da.Summary
		// The developer stage's primary repo id is the one that actually
		// succeeded (may not be run.RepoIDs[0] on a partial multi-repo
		// failure) — target the publisher at that repo, not the full set.
		if da.RepoID != "" {
			deliverRepoIDs = []string{da.RepoID}
		}
	}
	type deliverRepo struct {
		RepoID     string `json:"repo_id"`
		Branch     string `json:"branch"`
		HeadCommit string `json:"head_commit"`
	}
	var repos []deliverRepo
	for _, r := range da.Repos {
		// Deliver fully-verified repos AND those shipped test-unverified due to
		// an environment gap (build+vet passed; the caveat rides in the body).
		if r.TestsPassed || r.TestsUnverified {
			repos = append(repos, deliverRepo{RepoID: r.RepoID, Branch: r.BranchName, HeadCommit: r.HeadCommit})
		}
	}
	if len(repos) == 0 && branch != "" {
		// Fallback (single-repo / older artifact): use the primary fields.
		primaryRepo := da.RepoID
		if primaryRepo == "" && len(run.RepoIDs) > 0 {
			primaryRepo = run.RepoIDs[0]
		}
		repos = []deliverRepo{{RepoID: primaryRepo, Branch: branch, HeadCommit: headCommit}}
	}
	var title string
	if r := []rune(run.Spec); len(r) > 72 {
		title = string(r[:72])
	} else {
		title = run.Spec
	}
	body := run.Spec + "\n\n" + summary
	if da.QANote != "" {
		body += "\n\n---\n" + da.QANote
	}
	payload, err := json.Marshal(map[string]any{
		"repo_ids":    deliverRepoIDs,
		"branch":      branch,
		"head_commit": headCommit,
		"title":       title,
		"body":        body,
		"repos":       repos,
	})
	if err != nil {
		return fmt.Errorf("emit deliver: marshal: %w", err)
	}
	value, err := events.Marshal(&env, payload)
	if err != nil {
		return fmt.Errorf("emit deliver: %w", err)
	}
	if err := e.prod.Publish(ctx, kafka.TopicDeliverPR, runID, value); err != nil {
		return fmt.Errorf("emit deliver publish: %w", err)
	}
	return nil
}

func (e *Engine) emitGate(ctx context.Context, run Run, stage Stage, reason string) error {
	env, err := events.NewEnvelope(run.ID, "run.gated", "orchestrator", 1)
	if err != nil {
		return fmt.Errorf("emit gate: %w", err)
	}
	// A run can legitimately gate more than once (risk gate → approve → QA
	// exhaustion), so key on the gate stage — NewEnvelope's default key is
	// identical for every gate of a run and a dedup-on-key consumer would drop
	// the later gate notification. Mirrors emitStage's per-stage override.
	env.IdempotencyKey = fmt.Sprintf("%s:run.gated:%s", run.ID, string(stage))
	payload, err := json.Marshal(map[string]any{"stage": string(stage), "reason": reason})
	if err != nil {
		return fmt.Errorf("emit gate: marshal: %w", err)
	}
	value, err := events.Marshal(&env, payload)
	if err != nil {
		return fmt.Errorf("emit gate: %w", err)
	}
	if err := e.prod.Publish(ctx, kafka.TopicRunGated, run.ID, value); err != nil {
		return fmt.Errorf("emit gate publish: %w", err)
	}
	return nil
}

// expandFromAnalyzerProposal unions the analyzer's proposed_repos into the
// run's scope, updating run.RepoIDs in place. A no-op if the artifact is
// missing or proposes nothing new.
func (e *Engine) expandFromAnalyzerProposal(ctx context.Context, run *Run) error {
	arts, err := e.store.LoadArtifacts(ctx, run.ID)
	if err != nil {
		return fmt.Errorf("expand from analyzer proposal: %w", err)
	}
	raw, ok := arts[StageAnalyzing]
	if !ok {
		return nil
	}
	var a struct {
		ProposedRepos []string `json:"proposed_repos"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("expand from analyzer proposal: decode artifact: %w", err)
	}
	add := notIn(run.RepoIDs, a.ProposedRepos)
	if len(add) == 0 {
		return nil
	}
	merged, err := e.store.ExpandRepoIDs(ctx, run.ID, add)
	if err != nil {
		return fmt.Errorf("expand from analyzer proposal: %w", err)
	}
	run.RepoIDs = merged
	return nil
}

// resumeAfterAnalyzerGate advances a run parked at the analyzer proposal gate
// to the next stage, preserving the (possibly expanded) repo set.
func (e *Engine) resumeAfterAnalyzerGate(ctx context.Context, run Run) error {
	next, hasNext, err := NextStage(StageAnalyzing, run.Complexity)
	if err != nil {
		return fmt.Errorf("resume gate: %w", err)
	}
	if !hasNext {
		// Analyzing is never the terminal stage, but stay safe.
		if err := e.store.AdvanceRun(ctx, run.ID, StageAnalyzing, StatusDelivering, "repo gate resolved; delivering"); err != nil {
			return fmt.Errorf("resume gate: %w", err)
		}
		return e.emitDeliver(ctx, run.ID)
	}
	if err := e.store.AdvanceRun(ctx, run.ID, next, StatusRunning, "repo gate resolved; advance to "+string(next)); err != nil {
		return fmt.Errorf("resume gate: %w", err)
	}
	return e.emitStage(ctx, run, next)
}

// notIn returns the entries of candidates that are absent from base (deduped,
// order-preserving, dropping empties).
func notIn(base, candidates []string) []string {
	set := make(map[string]bool, len(base))
	for _, b := range base {
		set[b] = true
	}
	var out []string
	for _, c := range candidates {
		if c != "" && !set[c] {
			set[c] = true // also dedups candidates against themselves
			out = append(out, c)
		}
	}
	return out
}

func isNotFound(err error) bool { return errors.Is(err, ErrRunNotFound) }
