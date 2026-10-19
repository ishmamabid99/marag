package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/fin/multi-agent-rag/internal/codeagent"
	"github.com/fin/multi-agent-rag/internal/qa"
	"github.com/fin/multi-agent-rag/internal/sandbox"
)

// ErrDevExhausted marks a developer run whose deterministic QA never went green
// within the iteration budget. Terminal: the stage fails.
var ErrDevExhausted = errors.New("developer: deterministic QA not green after max iterations")

const maxDevIterations = 3

// DeveloperArtifact is the developer stage's output (subset of the design's
// DeveloperQAArtifact — the git-branch fields are filled once git-provider
// handoff lands).
type DeveloperArtifact struct {
	RepoID       string       `json:"repo_id"`
	BranchName   string       `json:"branch_name"`
	HeadCommit   string       `json:"head_commit"`
	FilesChanged []string     `json:"files_changed"`
	TestsPassed  bool         `json:"tests_passed"`
	Iterations   int          `json:"iterations"`
	FailingTests []string     `json:"failing_tests"`
	Summary      string       `json:"summary"`
	Repos        []RepoResult `json:"repos"`
	// QANote carries a QA caveat for the PR body — e.g. tests couldn't run
	// because an external service was unavailable, so the change ships
	// build+vet-verified but test-unverified. Empty when QA fully passed.
	QANote string `json:"qa_note,omitempty"`
}

// RepoResult is the per-repo outcome of a multi-repo developer run.
type RepoResult struct {
	RepoID       string   `json:"repo_id"`
	BranchName   string   `json:"branch_name"`
	HeadCommit   string   `json:"head_commit"`
	FilesChanged []string `json:"files_changed"`
	TestsPassed  bool     `json:"tests_passed"`
	// TestsUnverified is true when the change was shipped without a green test
	// run because tests needed an unavailable external service (build+vet still
	// passed). Distinct from TestsPassed (fully green) and Failure (blocked).
	TestsUnverified bool   `json:"tests_unverified,omitempty"`
	QANote          string `json:"qa_note,omitempty"`
	Failure         string `json:"failure,omitempty"`

	summary    string // per-repo agent summary (unexported → not serialized)
	iterations int    // per-repo iteration count (unexported → not serialized)
}

// shippable reports whether a repo result can be delivered as a PR: either its
// tests passed, or they couldn't run due to an environment gap (build+vet green).
func (r RepoResult) shippable() bool { return r.TestsPassed || r.TestsUnverified }

// Developer implements a change in a real cloned repo, iterating against
// deterministic QA, then pushes the work branch.
type Developer struct {
	agent      codeagent.CodeAgent
	escalation codeagent.CodeAgent // optional; more capable agent tried if the draft can't go green
	cheap      codeagent.CodeAgent // optional; cheapest agent used to lead trivial changes
	resolver   RepoResolver
	clone      func(ctx context.Context, source, token string) (*sandbox.LocalSandbox, error)
	execPrefix []string // jail launcher applied to each cloned sandbox's untrusted Run
	maxIter    int
}

// NewDeveloper wires a developer to a CodeAgent + repo resolver.
func NewDeveloper(agent codeagent.CodeAgent, resolver RepoResolver) *Developer {
	return &Developer{
		agent:    agent,
		resolver: resolver,
		clone:    sandbox.NewLocalFromRepo,
		maxIter:  maxDevIterations,
	}
}

// WithExecPrefix sets the jail launcher applied to untrusted command execution
// in each cloned sandbox (empty = direct exec; the caller is responsible for
// the fail-closed policy).
func (h *Developer) WithExecPrefix(prefix []string) *Developer {
	h.execPrefix = prefix
	return h
}

// WithEscalation sets a more capable agent (e.g. Opus) that takes over on the
// same sandbox when the draft agent can't get deterministic QA green within its
// iteration budget. nil (the default) means no escalation.
func (h *Developer) WithEscalation(a codeagent.CodeAgent) *Developer {
	h.escalation = a
	return h
}

// WithCheapAgent sets the cheapest agent (e.g. Haiku) used to LEAD trivial
// changes (per-file complexity routing). nil (the default) means trivial
// changes lead with the standard draft agent, as before.
func (h *Developer) WithCheapAgent(a codeagent.CodeAgent) *Developer {
	h.cheap = a
	return h
}

// routedTier aggregates the design's per-file complexity_hints into one model
// tier for the change: "complex" if any file is complex, else "standard" if any
// is standard, else "trivial". No design artifact ⟺ a trivial run (the design
// stage is skipped for trivial complexity), so absence routes to "trivial". An
// undecodable/empty design routes to the safe middle ("standard").
func routedTier(designJSON []byte) string {
	if len(designJSON) == 0 {
		return "trivial"
	}
	var d struct {
		FileChanges []struct {
			ComplexityHint string `json:"complexity_hint"`
		} `json:"file_changes"`
	}
	if err := json.Unmarshal(designJSON, &d); err != nil || len(d.FileChanges) == 0 {
		return "standard"
	}
	tier := "trivial"
	for _, fc := range d.FileChanges {
		switch fc.ComplexityHint {
		case "complex":
			return "complex"
		case "standard":
			tier = "standard"
		}
	}
	return tier
}

// selectAgents routes the lead + backup agents by the change's complexity tier
// (from the design's per-file hints), spending the expensive model only where
// warranted: complex → lead with the escalation agent (Opus) directly, skipping
// the doomed cheap/draft attempt; trivial → lead with the cheap agent (Haiku),
// draft as backup; standard → the default draft→escalation ladder. Every branch
// falls back to the standard agents when a tier's agent isn't wired.
func (h *Developer) selectAgents(designJSON []byte) (lead, backup codeagent.CodeAgent) {
	switch routedTier(designJSON) {
	case "complex":
		if h.escalation != nil {
			return h.escalation, nil
		}
	case "trivial":
		if h.cheap != nil {
			return h.cheap, h.agent
		}
	}
	return h.agent, h.escalation
}

func (h *Developer) Role() string { return "developer" }

// Handle resolves + clones the run's repo, creates the work branch, drives the
// CodeAgent to implement the change (re-running deterministic QA between
// attempts and feeding failures back), commits, pushes the branch, and
// returns a DeveloperArtifact. It returns ErrDevExhausted if QA never goes
// green.
func (h *Developer) Handle(ctx context.Context, in StageInput) (StageResult, error) {
	if len(in.RepoIDs) == 0 {
		return StageResult{}, fmt.Errorf("developer: no repo id in stage input")
	}
	if in.RunID == "" {
		return StageResult{}, fmt.Errorf("developer: empty run id")
	}
	branch := "marag/" + in.RunID

	var (
		results   []RepoResult
		priorWork strings.Builder
	)
	for _, repoID := range in.RepoIDs {
		r := h.implementRepo(ctx, in, repoID, branch, priorWork.String())
		results = append(results, r)
		if r.shippable() {
			fmt.Fprintf(&priorWork, "repo %s (branch %s): %s; files: %v\n", r.RepoID, r.BranchName, r.summary, r.FilesChanged)
		}
	}

	// Prefer a fully test-verified repo; fall back to one that's shippable but
	// test-unverified (build+vet green, tests blocked by a missing service).
	var primary *RepoResult
	for i := range results {
		if results[i].TestsPassed {
			primary = &results[i]
			break
		}
	}
	if primary == nil {
		for i := range results {
			if results[i].TestsUnverified {
				primary = &results[i]
				break
			}
		}
	}
	if primary == nil {
		reasons := make([]string, 0, len(results))
		for _, r := range results {
			reasons = append(reasons, fmt.Sprintf("%s: %s", r.RepoID, r.Failure))
		}
		return StageResult{}, fmt.Errorf("%w (all %d repos failed): %s", ErrDevExhausted, len(results), strings.Join(reasons, " | "))
	}

	art := DeveloperArtifact{
		RepoID:       primary.RepoID,
		BranchName:   primary.BranchName,
		HeadCommit:   primary.HeadCommit,
		FilesChanged: primary.FilesChanged,
		TestsPassed:  primary.TestsPassed,
		Iterations:   primary.iterations,
		FailingTests: nonNilStrings(nil),
		Repos:        results,
		Summary:      primary.summary,
		QANote:       primary.QANote,
	}
	outJSON, err := json.Marshal(art)
	if err != nil {
		return StageResult{}, fmt.Errorf("developer: marshal artifact: %w", err)
	}
	return StageResult{Stage: in.Stage, ArtifactJSON: outJSON}, nil
}

// implementRepo clones + branches + runs the deterministic-QA loop + pushes one
// repo. Any failure is captured in the returned RepoResult (Failure set,
// TestsPassed false); it never returns a Go error (best-effort fan-out).
func (h *Developer) implementRepo(ctx context.Context, in StageInput, repoID, branch, priorWork string) RepoResult {
	res := RepoResult{RepoID: repoID}
	co, err := h.resolver.Resolve(ctx, repoID)
	if err != nil {
		res.Failure = fmt.Sprintf("resolve: %v", err)
		return res
	}
	sb, err := h.clone(ctx, co.CloneURL, co.Token)
	if err != nil {
		res.Failure = fmt.Sprintf("clone: %v", err)
		return res
	}
	defer func() { _ = sb.Close() }()
	// Jail untrusted command execution (agent run_command + repo `go test`).
	sb.SetExecPrefix(h.execPrefix)

	// Base the work branch off the repo's configured base branch (if any)
	// rather than whatever ref the clone happened to check out.
	if co.BaseBranch != "" {
		if cr, rerr := sb.Run(ctx, "git", "checkout", co.BaseBranch); rerr != nil || cr.ExitCode != 0 {
			res.Failure = fmt.Sprintf("checkout base %q: %v %s", co.BaseBranch, rerr, cr.Stderr)
			return res
		}
	}
	if err := sb.CreateBranch(ctx, branch); err != nil {
		res.Failure = fmt.Sprintf("branch: %v", err)
		return res
	}

	// Choose lead/backup agents by the change's complexity tier (per-file
	// routing from the design hints): trivial → cheap lead, complex → Opus lead,
	// standard → draft→escalation. A semantic-QA retry (Attempt > 1 means
	// semantic QA rejected a prior attempt) overrides to lead with Opus, seeded
	// with the reviewer's issues — the draft already cleared build/test but
	// failed the semantic bar.
	lead, backup := h.selectAgents(in.PriorArtifacts["designing"])
	seed := ""
	if in.Attempt > 1 {
		seed = semanticQAFeedback(in.PriorArtifacts["qa"])
		if h.escalation != nil {
			lead, backup = h.escalation, nil
		}
	}
	lo := h.runLoop(ctx, lead, in, repoID, priorWork, seed, sb)
	if lo.err != nil {
		res.Failure = fmt.Sprintf("implement: %v", lo.err)
		return res
	}
	summary, qaRes, iter := lo.summary, lo.qa, lo.attempts
	lastFeedback := lo.feedback
	// Escalate to the backup agent only for genuine code failures — never for an
	// environment gap (a missing external service), which no code change fixes.
	if !qaRes.AllPassed && !qaRes.EnvUnavailable && backup != nil {
		esc := h.runLoop(ctx, backup, in, repoID, priorWork, escalationFeedback(lo.feedback, lo.attempts), sb)
		if esc.err != nil {
			res.Failure = fmt.Sprintf("escalation: %v", esc.err)
			return res
		}
		iter += esc.attempts
		if esc.qa.AllPassed {
			qaRes, summary = esc.qa, esc.summary
		} else {
			lastFeedback = esc.feedback
			qaRes = esc.qa
		}
	}
	res.iterations = iter
	res.summary = summary
	if !qaRes.AllPassed && !qaRes.EnvUnavailable {
		// Genuine code failure — preserve the actual build/vet/test errors
		// (tail) so it's diagnosable after the disposable sandbox is gone.
		res.Failure = fmt.Sprintf("deterministic QA not green after %d attempts:\n%s", iter, tailN(lastFeedback, 1200))
		return res
	}
	// From here the change is shippable: either fully green, or build+vet green
	// with tests blocked by an unavailable external service (env gap). In the
	// env-gap case we proceed to a PR but record a caveat rather than blocking —
	// the sandbox can't provide the service, and no code change would fix that.
	if qaRes.EnvUnavailable {
		res.TestsUnverified = true
		res.QANote = fmt.Sprintf("⚠️ Deterministic QA: build + `go vet` passed, but tests could NOT run because an external service is unavailable in the sandbox (e.g. a database). Proceeding **without test verification** — please run the test suite in an environment with its dependencies. Detail:\n```\n%s\n```", tailN(lastFeedback, 800))
	}

	sha, err := sb.Commit(ctx, "developer: "+in.Spec)
	if err != nil {
		res.Failure = fmt.Sprintf("commit: %v", err)
		return res
	}
	if err := sb.Push(ctx, "origin", branch, true); err != nil {
		res.Failure = fmt.Sprintf("push: %v", err)
		return res
	}
	// Derive the changed-file list from git (authoritative) rather than trusting
	// the agent's self-report — Commit stages everything (git add -A), so the
	// commit is the source of truth that flows into the PR file list.
	files, err := committedFiles(ctx, sb, sha)
	if err != nil {
		res.Failure = fmt.Sprintf("list files: %v", err)
		return res
	}
	res.BranchName = branch
	res.HeadCommit = sha
	res.FilesChanged = files
	res.TestsPassed = qaRes.AllPassed // false in the env-gap case (TestsUnverified set)
	return res
}

// loopOutcome is the result of running one agent through the deterministic-QA
// loop on a sandbox.
type loopOutcome struct {
	summary  string
	qa       qa.Result
	attempts int
	feedback string // last deterministic failure, if it never went green
	err      error  // fatal agent/qa error (aborts the repo)
}

// runLoop drives one agent up to h.maxIter attempts against deterministic QA,
// feeding each failure back as the next attempt's feedback, starting from the
// given feedback. It stops early on the first green result.
func (h *Developer) runLoop(ctx context.Context, ag codeagent.CodeAgent, in StageInput, repoID, priorWork, feedback string, sb sandbox.Sandbox) loopOutcome {
	var lo loopOutcome
	for lo.attempts < h.maxIter {
		lo.attempts++
		out, err := ag.Implement(ctx, codeagent.Task{
			Spec: in.Spec, RepoIDs: in.RepoIDs, TargetRepoID: repoID,
			PriorRepoWork: priorWork, Feedback: feedback, Attempt: lo.attempts,
		}, sb)
		if err != nil {
			lo.err = err
			return lo
		}
		lo.summary = out.Summary
		qaRes, err := qa.RunChecks(ctx, sb)
		if err != nil {
			lo.err = err
			return lo
		}
		lo.qa = qaRes
		if qaRes.AllPassed {
			return lo
		}
		if qaRes.EnvUnavailable {
			// The tests need an external service the sandbox can't provide;
			// retrying the agent can't fix that. Stop now with the detail.
			lo.feedback = deterministicFeedback(qaRes)
			return lo
		}
		feedback = deterministicFeedback(qaRes)
		lo.feedback = feedback
	}
	return lo
}

// semanticQAFeedback renders the semantic QA reviewer's rejection into feedback
// for a re-development. Returns "" if there's no usable (failing) QA artifact.
func semanticQAFeedback(qaJSON []byte) string {
	if len(qaJSON) == 0 {
		return ""
	}
	var q QAArtifact
	if err := json.Unmarshal(qaJSON, &q); err != nil || q.QAPassed {
		return ""
	}
	var b strings.Builder
	b.WriteString("A previous implementation passed build/test but FAILED semantic QA review. Address these issues:\n")
	for _, iss := range q.Issues {
		fmt.Fprintf(&b, "- %s\n", iss)
	}
	if q.Rationale != "" {
		fmt.Fprintf(&b, "Reviewer rationale: %s", q.Rationale)
	}
	return b.String()
}

// escalationFeedback frames the draft's failure for the escalation agent, which
// works on the same sandbox (so the draft's diff is already present).
func escalationFeedback(lastFeedback string, draftAttempts int) string {
	return fmt.Sprintf("A faster draft model attempted this %d time(s) and could not get deterministic QA green. You are the escalation developer — the draft's changes are already in this workspace; diagnose the ROOT cause and fix it. Last failure:\n\n%s", draftAttempts, lastFeedback)
}

// deterministicFeedback renders a compact failure summary for the next attempt.
func deterministicFeedback(r qa.Result) string {
	var b strings.Builder
	if !r.Build.Passed {
		b.WriteString("BUILD FAILED:\n")
		b.WriteString(r.Build.Output)
		return b.String()
	}
	if !r.Vet.Passed {
		b.WriteString("VET FAILED:\n")
		b.WriteString(r.Vet.Output)
		b.WriteString("\n")
	}
	if !r.Test.Passed {
		b.WriteString("TESTS FAILED: ")
		b.WriteString(strings.Join(r.FailingTests, ", "))
		b.WriteString("\n")
		b.WriteString(r.Test.Output)
	}
	return b.String()
}

// committedFiles lists the paths in commit sha (authoritative changed-file
// list). Returns a non-nil (possibly empty) sorted slice.
func committedFiles(ctx context.Context, sb sandbox.Sandbox, sha string) ([]string, error) {
	res, err := sb.Run(ctx, "git", "diff-tree", "--no-commit-id", "--name-only", "-r", sha)
	if err != nil {
		return nil, fmt.Errorf("git diff-tree: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("git diff-tree exit %d: %s", res.ExitCode, res.Stderr)
	}
	files := []string{}
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	sort.Strings(files)
	return files, nil
}

// nonNilStrings returns s, or an empty (non-nil) slice so JSON marshals [] not null.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// tailN returns the last n characters of s (whole string if shorter), prefixed
// with an ellipsis when truncated. Keeps the failure reason bounded while
// preserving the most recent (usually most relevant) build/test output.
func tailN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
