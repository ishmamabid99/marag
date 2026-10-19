package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/fin/multi-agent-rag/internal/events"
	"github.com/fin/multi-agent-rag/internal/kafka"
)

// fakeProducer records published records.
type fakeProducer struct {
	mu   sync.Mutex
	recs []pubRec
}
type pubRec struct {
	topic, key string
	value      []byte
}

func (f *fakeProducer) Publish(_ context.Context, topic, key string, value []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recs = append(f.recs, pubRec{topic, key, value})
	return nil
}
func (f *fakeProducer) last() pubRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.recs[len(f.recs)-1]
}
func (f *fakeProducer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.recs)
}

// decodeStage extracts the envelope type + payload from the last record.
func decodeStage(t *testing.T, r pubRec) (string, string) {
	t.Helper()
	env, payload, err := events.Unmarshal(r.value)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return env.Type, string(payload)
}

func TestEngineStartAndAdvance(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fp := &fakeProducer{}
	e := NewEngine(s, fp)

	if err := e.StartRun(ctx, "run-1", "add feature", []string{"repo-a"}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	// Emitted a stage.requested to the analyzing role.
	if fp.last().topic != kafka.TopicStageRequested {
		t.Fatalf("topic = %s", fp.last().topic)
	}
	typ, payload := decodeStage(t, fp.last())
	if typ != "stage.requested" || !contains(payload, "requirement-analyzer") {
		t.Fatalf("start emitted type=%s payload=%s", typ, payload)
	}

	// analyzing completes → advance to viability.
	if err := e.OnStageCompleted(ctx, StageCompleted{RunID: "run-1", Stage: StageAnalyzing}); err != nil {
		t.Fatalf("OnStageCompleted analyzing: %v", err)
	}
	run, _ := s.GetRun(ctx, "run-1")
	if run.CurrentStage != StageViability {
		t.Fatalf("stage = %s, want viability", run.CurrentStage)
	}
	_, payload = decodeStage(t, fp.last())
	if !contains(payload, "viability-checker") {
		t.Fatalf("expected viability-checker request, got %s", payload)
	}

	// viability completes with confirmed trivial → skips to developing.
	if err := e.OnStageCompleted(ctx, StageCompleted{RunID: "run-1", Stage: StageViability, ConfirmedComplexity: ComplexityTrivial}); err != nil {
		t.Fatalf("OnStageCompleted viability: %v", err)
	}
	run, _ = s.GetRun(ctx, "run-1")
	if run.Complexity != ComplexityTrivial || run.CurrentStage != StageDeveloping {
		t.Fatalf("after viability: complexity=%s stage=%s", run.Complexity, run.CurrentStage)
	}
}

func TestEngineStartRunEmitsSpec(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fp := &fakeProducer{}
	e := NewEngine(s, fp)

	if err := e.StartRun(ctx, "run-spec", "add currency validation", []string{"payment-svc"}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	_, payload := decodeStage(t, fp.last())
	var got struct {
		Role    string   `json:"role"`
		Stage   string   `json:"stage"`
		Spec    string   `json:"spec"`
		RepoIDs []string `json:"repo_ids"`
	}
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got.Spec != "add currency validation" {
		t.Fatalf("spec = %q, want %q", got.Spec, "add currency validation")
	}
	if !containsStr(got.RepoIDs, "payment-svc") {
		t.Fatalf("repo_ids = %v, want to contain %q", got.RepoIDs, "payment-svc")
	}
	if got.Role != "requirement-analyzer" {
		t.Fatalf("role = %q, want %q", got.Role, "requirement-analyzer")
	}
	if got.Stage != "analyzing" {
		t.Fatalf("stage = %q, want %q", got.Stage, "analyzing")
	}
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestEngineDuplicateCompletionReemits(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fp := &fakeProducer{}
	e := NewEngine(s, fp)
	_ = e.StartRun(ctx, "run-2", "spec", nil)
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-2", Stage: StageAnalyzing}) // → viability
	countAfterFirst := fp.count()
	stageAfterFirst, _ := s.GetRun(ctx, "run-2")

	// Duplicate analyzing completion (replay): must NOT double-advance.
	if err := e.OnStageCompleted(ctx, StageCompleted{RunID: "run-2", Stage: StageAnalyzing}); err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	run, _ := s.GetRun(ctx, "run-2")
	if run.CurrentStage != stageAfterFirst.CurrentStage {
		t.Fatalf("duplicate advanced stage to %s (was %s)", run.CurrentStage, stageAfterFirst.CurrentStage)
	}
	// It re-emitted the current stage's request (recovery), so count increased by 1.
	if fp.count() != countAfterFirst+1 {
		t.Fatalf("expected 1 re-emit, count %d → %d", countAfterFirst, fp.count())
	}
	_, payload := decodeStage(t, fp.last())
	if !contains(payload, "viability-checker") {
		t.Fatalf("re-emit should target current stage viability, got %s", payload)
	}
}

func TestEngineQADoneEmitsDeliver(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fp := &fakeProducer{}
	e := NewEngine(s, fp)
	_ = e.StartRun(ctx, "run-3", "spec", nil)
	// Drive a trivial run: analyzing → viability(trivial) → developing → qa → deliver.
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-3", Stage: StageAnalyzing})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-3", Stage: StageViability, ConfirmedComplexity: ComplexityTrivial})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-3", Stage: StageDeveloping})
	if err := e.OnStageCompleted(ctx, StageCompleted{RunID: "run-3", Stage: StageQA, QAPassed: true}); err != nil {
		t.Fatalf("qa complete: %v", err)
	}
	run, _ := s.GetRun(ctx, "run-3")
	if run.Status != StatusDelivering {
		t.Fatalf("status = %s, want delivering", run.Status)
	}
	if fp.last().topic != kafka.TopicDeliverPR {
		t.Fatalf("last topic = %s, want %s", fp.last().topic, kafka.TopicDeliverPR)
	}
}

func TestEngineDeliverCarriesBranch(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fp := &fakeProducer{}
	e := NewEngine(s, fp)
	_ = e.StartRun(ctx, "run-dl", "add feature", []string{"repo-a", "repo-b"})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-dl", Stage: StageAnalyzing})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-dl", Stage: StageViability, ConfirmedComplexity: ComplexityTrivial})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-dl", Stage: StageDeveloping,
		Artifact: []byte(`{"repo_id":"repo-b","branch_name":"marag/run-dl","head_commit":"abc1234","summary":"did it"}`)})
	// qa passes → deliver.
	if err := e.OnStageCompleted(ctx, StageCompleted{RunID: "run-dl", Stage: StageQA, QAPassed: true,
		Artifact: []byte(`{"qa_passed":true}`)}); err != nil {
		t.Fatalf("qa: %v", err)
	}
	if fp.last().topic != kafka.TopicDeliverPR {
		t.Fatalf("last topic = %s", fp.last().topic)
	}
	_, payload := decodeStage(t, fp.last())
	var p struct {
		RepoIDs    []string `json:"repo_ids"`
		Branch     string   `json:"branch"`
		HeadCommit string   `json:"head_commit"`
		Title      string   `json:"title"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Branch != "marag/run-dl" || p.HeadCommit != "abc1234" || p.Title == "" {
		t.Fatalf("deliver payload = %+v", p)
	}
	// emitDeliver must target the repo that actually succeeded (carried in the
	// developing artifact's repo_id), not blindly run.RepoIDs.
	if len(p.RepoIDs) != 1 || p.RepoIDs[0] != "repo-b" {
		t.Fatalf("repo_ids = %v, want [repo-b]", p.RepoIDs)
	}
}

func TestEngineDeliverCarriesAllSuccessfulRepos(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fp := &fakeProducer{}
	e := NewEngine(s, fp)
	_ = e.StartRun(ctx, "run-md", "multi feature", []string{"repo-a", "repo-b"})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-md", Stage: StageAnalyzing})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-md", Stage: StageViability, ConfirmedComplexity: ComplexityTrivial})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-md", Stage: StageDeveloping,
		Artifact: []byte(`{"repo_id":"repo-a","branch_name":"marag/run-md","repos":[
			{"repo_id":"repo-a","branch_name":"marag/run-md","head_commit":"aaa111","tests_passed":true},
			{"repo_id":"repo-b","branch_name":"marag/run-md","head_commit":"bbb222","tests_passed":true}]}`)})
	if err := e.OnStageCompleted(ctx, StageCompleted{RunID: "run-md", Stage: StageQA, QAPassed: true, Artifact: []byte(`{"qa_passed":true}`)}); err != nil {
		t.Fatalf("qa: %v", err)
	}
	_, payload := decodeStage(t, fp.last())
	var p struct {
		Repos []struct {
			RepoID     string `json:"repo_id"`
			Branch     string `json:"branch"`
			HeadCommit string `json:"head_commit"`
		} `json:"repos"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(p.Repos) != 2 {
		t.Fatalf("repos = %+v", p.Repos)
	}
	if p.Repos[0].RepoID != "repo-a" || p.Repos[1].RepoID != "repo-b" || p.Repos[1].HeadCommit != "bbb222" {
		t.Fatalf("repos = %+v", p.Repos)
	}
}

func TestEngineFailRegistersAndCompensates(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fp := &fakeProducer{}
	e := NewEngine(s, fp)
	_ = e.StartRun(ctx, "run-4", "spec", nil)
	_ = s.RegisterCompensation(ctx, Compensation{RunID: "run-4", Stage: StageDeveloping, Action: "delete_branch", ParamsJSON: "{}"})

	if err := e.OnStageFailed(ctx, "run-4", StageViability, "infeasible"); err != nil {
		t.Fatalf("OnStageFailed: %v", err)
	}
	run, _ := s.GetRun(ctx, "run-4")
	if run.Status != StatusFailed {
		t.Fatalf("status = %s, want failed", run.Status)
	}
	pending, _ := s.PendingCompensations(ctx, "run-4")
	if len(pending) != 0 {
		t.Fatalf("compensations should be executed, %d pending", len(pending))
	}
}

type fakeExecutor struct{ calls []string }

func (f *fakeExecutor) Execute(_ context.Context, action, params string) error {
	f.calls = append(f.calls, action+"|"+params)
	return nil
}

func TestEngineExecutesCompensationsInReverseOrder(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fp := &fakeProducer{}
	ex := &fakeExecutor{}
	e := NewEngine(s, fp).WithCompensationExecutor(ex)
	_ = e.StartRun(ctx, "run-c", "spec", []string{"repo-a"})
	// Register in publisher order: delete_branch then close_pr.
	_ = s.RegisterCompensation(ctx, Compensation{RunID: "run-c", Stage: StageDeveloping, Action: "delete_branch", ParamsJSON: `{"branch":"b","repo_id":"repo-a"}`})
	_ = s.RegisterCompensation(ctx, Compensation{RunID: "run-c", Stage: StageDeveloping, Action: "close_pr", ParamsJSON: `{"pr_id":"1","repo_id":"repo-a"}`})

	if err := e.OnStageFailed(ctx, "run-c", StageDeveloping, "boom"); err != nil {
		t.Fatalf("OnStageFailed: %v", err)
	}
	run, _ := s.GetRun(ctx, "run-c")
	if run.Status != StatusFailed {
		t.Fatalf("status = %s", run.Status)
	}
	// Executed reverse-registration order: close_pr first, then delete_branch.
	if len(ex.calls) != 2 || ex.calls[0][:8] != "close_pr" {
		t.Fatalf("executor calls = %+v", ex.calls)
	}
	// All marked executed.
	pending, _ := s.PendingCompensations(ctx, "run-c")
	if len(pending) != 0 {
		t.Fatalf("pending = %d, want 0", len(pending))
	}
}

// failingExecutor always errors, to prove a failed rollback is NOT marked
// executed (M2) — it stays pending for a future retry, not silently orphaned.
type failingExecutor struct{}

func (failingExecutor) Execute(_ context.Context, _, _ string) error {
	return fmt.Errorf("provider down")
}

func TestEngineKeepsFailedCompensationsPending(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	e := NewEngine(s, &fakeProducer{}).WithCompensationExecutor(failingExecutor{})
	_ = e.StartRun(ctx, "run-m2", "spec", []string{"repo-a"})
	_ = s.RegisterCompensation(ctx, Compensation{RunID: "run-m2", Stage: StageDeveloping, Action: "close_pr", ParamsJSON: `{"pr_id":"1"}`})

	// OnStageFailed runs compensations; the executor fails, so firstErr propagates.
	if err := e.OnStageFailed(ctx, "run-m2", StageDeveloping, "boom"); err == nil {
		t.Fatal("expected the failed compensation to surface an error")
	}
	// The compensation must remain PENDING (not marked executed).
	pending, _ := s.PendingCompensations(ctx, "run-m2")
	if len(pending) != 1 {
		t.Fatalf("pending = %d, want 1 (failed comp must stay retryable)", len(pending))
	}
}

func TestEngineIgnoresCompletionAfterTerminal(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fp := &fakeProducer{}
	e := NewEngine(s, fp)

	if err := e.StartRun(ctx, "run-term", "spec", nil); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	countAfterStart := fp.count()

	if err := e.OnStageFailed(ctx, "run-term", StageAnalyzing, "boom"); err != nil {
		t.Fatalf("OnStageFailed: %v", err)
	}
	run, _ := s.GetRun(ctx, "run-term")
	if run.Status != StatusFailed {
		t.Fatalf("status after fail = %s, want failed", run.Status)
	}
	stageAfterFail := run.CurrentStage

	// A duplicate/out-of-order completion for the failing stage must be IGNORED,
	// not resurrect the terminal run.
	if err := e.OnStageCompleted(ctx, StageCompleted{RunID: "run-term", Stage: StageAnalyzing}); err != nil {
		t.Fatalf("OnStageCompleted after terminal: %v", err)
	}
	run, _ = s.GetRun(ctx, "run-term")
	if run.Status != StatusFailed {
		t.Fatalf("run resurrected to %s, want still failed", run.Status)
	}
	if run.CurrentStage != stageAfterFail {
		t.Fatalf("current_stage changed to %s (was %s)", run.CurrentStage, stageAfterFail)
	}
	if fp.count() != countAfterStart {
		t.Fatalf("ignored completion emitted %d new events, want 0", fp.count()-countAfterStart)
	}
}

func TestEngineRiskGateParksRun(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fp := &fakeProducer{}
	e := NewEngine(s, fp)
	_ = e.StartRun(ctx, "run-gate", "spec", []string{"r"})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-gate", Stage: StageAnalyzing})
	// standard complexity so the pipeline includes risk before designing.
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-gate", Stage: StageViability, ConfirmedComplexity: ComplexityStandard})
	countBefore := fp.count()

	if err := e.OnStageCompleted(ctx, StageCompleted{RunID: "run-gate", Stage: StageRisk, GateRecommended: true}); err != nil {
		t.Fatalf("risk complete: %v", err)
	}
	run, _ := s.GetRun(ctx, "run-gate")
	if run.Status != StatusGatedWaitingHuman || run.CurrentStage != StageRisk {
		t.Fatalf("after risk gate: status=%s stage=%s", run.Status, run.CurrentStage)
	}
	// Emitted a run.gated event and did NOT emit the next stage request.
	if fp.count() != countBefore+1 || fp.last().topic != kafka.TopicRunGated {
		t.Fatalf("gate should emit exactly one run.gated; last topic=%s count %d->%d", fp.last().topic, countBefore, fp.count())
	}
}

func TestEngineQALoopThenPass(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fp := &fakeProducer{}
	e := NewEngine(s, fp)
	_ = e.StartRun(ctx, "run-qa", "spec", nil)
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-qa", Stage: StageAnalyzing})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-qa", Stage: StageViability, ConfirmedComplexity: ComplexityTrivial})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-qa", Stage: StageDeveloping})

	// QA fails once → loops back to developing, counter = 1.
	if err := e.OnStageCompleted(ctx, StageCompleted{RunID: "run-qa", Stage: StageQA, QAPassed: false}); err != nil {
		t.Fatalf("qa fail: %v", err)
	}
	run, _ := s.GetRun(ctx, "run-qa")
	if run.CurrentStage != StageDeveloping || run.QAIterations != 1 || run.Status != StatusRunning {
		t.Fatalf("after qa fail: stage=%s iters=%d status=%s", run.CurrentStage, run.QAIterations, run.Status)
	}
	_, payload := decodeStage(t, fp.last())
	if !contains(payload, "developer") {
		t.Fatalf("qa fail should re-emit developer, got %s", payload)
	}

	// developer re-runs, QA passes → deliver.
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-qa", Stage: StageDeveloping})
	if err := e.OnStageCompleted(ctx, StageCompleted{RunID: "run-qa", Stage: StageQA, QAPassed: true}); err != nil {
		t.Fatalf("qa pass: %v", err)
	}
	run, _ = s.GetRun(ctx, "run-qa")
	if run.Status != StatusDelivering {
		t.Fatalf("after qa pass: status=%s want delivering", run.Status)
	}
	if fp.last().topic != kafka.TopicDeliverPR {
		t.Fatalf("last topic=%s want deliver.pr", fp.last().topic)
	}
}

// deliverThenReturn drives a trivial run all the way to StatusDelivering.
func deliverToDelivering(t *testing.T, ctx context.Context, e *Engine, s *Store, id string) {
	t.Helper()
	if err := e.StartRun(ctx, id, "spec", nil); err != nil {
		t.Fatalf("start run: %v", err)
	}
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: id, Stage: StageAnalyzing})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: id, Stage: StageViability, ConfirmedComplexity: ComplexityTrivial})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: id, Stage: StageDeveloping})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: id, Stage: StageQA, QAPassed: true})
	run, _ := s.GetRun(ctx, id)
	if run.Status != StatusDelivering {
		t.Fatalf("precondition: status=%s want delivering", run.Status)
	}
}

func TestEnginePROpenedCompletes(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	e := NewEngine(s, &fakeProducer{})

	// Happy path: a delivering run whose PR opens becomes completed.
	deliverToDelivering(t, ctx, e, s, "run-pr")
	if err := e.OnPROpened(ctx, PROpened{RunID: "run-pr", Opened: []PRRef{{RepoID: "r1", PRID: "1", URL: "https://x/pull/1"}}}); err != nil {
		t.Fatalf("on pr opened: %v", err)
	}
	run, _ := s.GetRun(ctx, "run-pr")
	if run.Status != StatusCompleted {
		t.Fatalf("after pr opened: status=%s want completed", run.Status)
	}
	if run.CurrentStage != StageQA {
		t.Fatalf("completed run should stay parked at qa, got stage=%s", run.CurrentStage)
	}

	// Idempotent: a duplicate run.pr.opened is a no-op, stays completed.
	if err := e.OnPROpened(ctx, PROpened{RunID: "run-pr", Opened: []PRRef{{RepoID: "r1", PRID: "1", URL: "https://x/pull/1"}}}); err != nil {
		t.Fatalf("dup pr opened: %v", err)
	}
	run, _ = s.GetRun(ctx, "run-pr")
	if run.Status != StatusCompleted {
		t.Fatalf("after dup: status=%s want completed", run.Status)
	}

	// All-repos-failed (empty Opened) must NOT complete: the publisher retries
	// that case, so the run stays delivering.
	deliverToDelivering(t, ctx, e, s, "run-pr-empty")
	if err := e.OnPROpened(ctx, PROpened{RunID: "run-pr-empty", Opened: nil}); err != nil {
		t.Fatalf("empty pr opened: %v", err)
	}
	run, _ = s.GetRun(ctx, "run-pr-empty")
	if run.Status != StatusDelivering {
		t.Fatalf("empty opened should leave delivering, got %s", run.Status)
	}

	// A PR-opened event for a run that never reached delivery is ignored.
	_ = e.StartRun(ctx, "run-pr-running", "spec", nil)
	if err := e.OnPROpened(ctx, PROpened{RunID: "run-pr-running", Opened: []PRRef{{RepoID: "r1", PRID: "9", URL: "u"}}}); err != nil {
		t.Fatalf("running pr opened: %v", err)
	}
	run, _ = s.GetRun(ctx, "run-pr-running")
	if run.Status != StatusRunning {
		t.Fatalf("non-delivering run should be untouched, got %s", run.Status)
	}
}

func TestAnalyzerProposedReposGates(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fp := &fakeProducer{}
	e := NewEngine(s, fp)
	_ = e.StartRun(ctx, "run-prop", "spec", []string{"payments"})
	art := []byte(`{"summary":"s","preliminary_complexity":"standard","affected_repos":["payments"],"proposed_repos":["ledger"]}`)
	if err := e.OnStageCompleted(ctx, StageCompleted{RunID: "run-prop", Stage: StageAnalyzing, Artifact: art, ProposedRepos: []string{"ledger"}}); err != nil {
		t.Fatalf("analyzer completed: %v", err)
	}
	run, _ := s.GetRun(ctx, "run-prop")
	if run.Status != StatusGatedWaitingHuman {
		t.Fatalf("status=%s want gated", run.Status)
	}
	if fp.last().topic != kafka.TopicRunGated {
		t.Fatalf("last topic=%s want run.gated", fp.last().topic)
	}
	// Approve → repos expanded, pipeline resumes at the next stage.
	if err := e.ResumeGate(ctx, "run-prop", true); err != nil {
		t.Fatalf("resume approve: %v", err)
	}
	run, _ = s.GetRun(ctx, "run-prop")
	if !containsStr(run.RepoIDs, "ledger") {
		t.Fatalf("after approve repos=%v want ledger included", run.RepoIDs)
	}
	if run.Status != StatusRunning {
		t.Fatalf("after approve status=%s want running", run.Status)
	}
}

func TestAnalyzerProposedReposRejectProceeds(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fp := &fakeProducer{}
	e := NewEngine(s, fp)
	_ = e.StartRun(ctx, "run-rej", "spec", []string{"payments"})
	art := []byte(`{"summary":"s","preliminary_complexity":"standard","affected_repos":["payments"],"proposed_repos":["ledger"]}`)
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-rej", Stage: StageAnalyzing, Artifact: art, ProposedRepos: []string{"ledger"}})
	// Reject → proceed with the ORIGINAL repos, run not failed.
	if err := e.ResumeGate(ctx, "run-rej", false); err != nil {
		t.Fatalf("resume reject: %v", err)
	}
	run, _ := s.GetRun(ctx, "run-rej")
	if run.Status != StatusRunning {
		t.Fatalf("after reject status=%s want running (proceed with original)", run.Status)
	}
	if containsStr(run.RepoIDs, "ledger") {
		t.Fatalf("reject must NOT expand repos, got %v", run.RepoIDs)
	}
}

func TestEngineQAExhaustionGates(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fp := &fakeProducer{}
	e := NewEngine(s, fp)
	_ = e.StartRun(ctx, "run-ex", "spec", nil)
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-ex", Stage: StageAnalyzing})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-ex", Stage: StageViability, ConfirmedComplexity: ComplexityTrivial})

	// Drive MaxQAIterations failing rounds: each is developing-complete then qa-fail.
	for i := 0; i < MaxQAIterations; i++ {
		_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-ex", Stage: StageDeveloping})
		_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-ex", Stage: StageQA, QAPassed: false})
	}
	run, _ := s.GetRun(ctx, "run-ex")
	if run.Status != StatusGatedWaitingHuman || run.CurrentStage != StageQA {
		t.Fatalf("after exhaustion: status=%s stage=%s want gated@qa", run.Status, run.CurrentStage)
	}
	if run.QAIterations != MaxQAIterations {
		t.Fatalf("iters=%d want %d", run.QAIterations, MaxQAIterations)
	}
	if fp.last().topic != kafka.TopicRunGated {
		t.Fatalf("last topic=%s want run.gated", fp.last().topic)
	}
}

func TestEngineResumeGateApproveContinues(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fp := &fakeProducer{}
	e := NewEngine(s, fp)
	_ = e.StartRun(ctx, "run-ra", "spec", []string{"r"})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-ra", Stage: StageAnalyzing})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-ra", Stage: StageViability, ConfirmedComplexity: ComplexityStandard})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-ra", Stage: StageRisk, GateRecommended: true})

	if err := e.ResumeGate(ctx, "run-ra", true); err != nil {
		t.Fatalf("ResumeGate approve: %v", err)
	}
	run, _ := s.GetRun(ctx, "run-ra")
	// standard pipeline: risk → designing.
	if run.Status != StatusRunning || run.CurrentStage != StageDesigning {
		t.Fatalf("after approve: status=%s stage=%s want running@designing", run.Status, run.CurrentStage)
	}
	_, payload := decodeStage(t, fp.last())
	if !contains(payload, "system-designer") {
		t.Fatalf("approve should emit designing request, got %s", payload)
	}
}

func TestEngineResumeGateRejectFailsAndCompensates(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fp := &fakeProducer{}
	e := NewEngine(s, fp)
	_ = e.StartRun(ctx, "run-rr", "spec", nil)
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-rr", Stage: StageAnalyzing})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-rr", Stage: StageViability, ConfirmedComplexity: ComplexityStandard})
	_ = s.RegisterCompensation(ctx, Compensation{RunID: "run-rr", Stage: StageRisk, Action: "delete_branch", ParamsJSON: "{}"})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-rr", Stage: StageRisk, GateRecommended: true})

	if err := e.ResumeGate(ctx, "run-rr", false); err != nil {
		t.Fatalf("ResumeGate reject: %v", err)
	}
	run, _ := s.GetRun(ctx, "run-rr")
	if run.Status != StatusFailed {
		t.Fatalf("after reject: status=%s want failed", run.Status)
	}
	pending, _ := s.PendingCompensations(ctx, "run-rr")
	if len(pending) != 0 {
		t.Fatalf("compensations should be executed, %d pending", len(pending))
	}
}

func TestEngineResumeGateIgnoresNonGated(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fp := &fakeProducer{}
	e := NewEngine(s, fp)
	_ = e.StartRun(ctx, "run-ng", "spec", nil) // running, not gated
	countBefore := fp.count()
	if err := e.ResumeGate(ctx, "run-ng", true); err != nil {
		t.Fatalf("ResumeGate non-gated: %v", err)
	}
	run, _ := s.GetRun(ctx, "run-ng")
	if run.Status != StatusRunning {
		t.Fatalf("non-gated run changed to %s", run.Status)
	}
	if fp.count() != countBefore {
		t.Fatalf("non-gated resume emitted %d events, want 0", fp.count()-countBefore)
	}
}

// A run can gate twice (risk gate → approve → QA exhaustion). The two run.gated
// events must carry distinct idempotency keys so a dedup-on-key consumer does
// not drop the second gate notification.
func TestEngineGateEventsHaveDistinctKeys(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fp := &fakeProducer{}
	e := NewEngine(s, fp)
	_ = e.StartRun(ctx, "run-2g", "spec", []string{"r"})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-2g", Stage: StageAnalyzing})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-2g", Stage: StageViability, ConfirmedComplexity: ComplexityStandard})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-2g", Stage: StageRisk, GateRecommended: true}) // gate #1 (risk)
	_ = e.ResumeGate(ctx, "run-2g", true)                                                                 // approve → designing
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-2g", Stage: StageDesigning})
	for i := 0; i < MaxQAIterations; i++ { // exhaust QA → gate #2 (qa)
		_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-2g", Stage: StageDeveloping})
		_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-2g", Stage: StageQA, QAPassed: false})
	}

	var keys []string
	fp.mu.Lock()
	for _, r := range fp.recs {
		if r.topic == kafka.TopicRunGated {
			env, _, err := events.Unmarshal(r.value)
			if err != nil {
				fp.mu.Unlock()
				t.Fatalf("unmarshal run.gated: %v", err)
			}
			keys = append(keys, env.IdempotencyKey)
		}
	}
	fp.mu.Unlock()
	if len(keys) != 2 {
		t.Fatalf("want 2 run.gated events, got %d: %v", len(keys), keys)
	}
	if keys[0] == keys[1] {
		t.Fatalf("gate idempotency keys collide: %q", keys[0])
	}
}

func TestEngineChainsPriorArtifacts(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fp := &fakeProducer{}
	e := NewEngine(s, fp)
	_ = e.StartRun(ctx, "run-ch", "spec", nil)
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-ch", Stage: StageAnalyzing, Artifact: []byte(`{"preliminary_complexity":"trivial"}`)})
	// viability confirms trivial → next stage is developing.
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-ch", Stage: StageViability, ConfirmedComplexity: ComplexityTrivial, Artifact: []byte(`{"feasible":true}`)})
	// developing completes with an artifact → the qa stage.requested must carry it.
	if err := e.OnStageCompleted(ctx, StageCompleted{RunID: "run-ch", Stage: StageDeveloping, Artifact: []byte(`{"tests_passed":true,"summary":"did it"}`)}); err != nil {
		t.Fatalf("developing complete: %v", err)
	}
	_, payload := decodeStage(t, fp.last())
	var got struct {
		Stage          string                     `json:"stage"`
		PriorArtifacts map[string]json.RawMessage `json:"prior_artifacts"`
	}
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Stage != "qa" {
		t.Fatalf("next stage = %s, want qa", got.Stage)
	}
	dev, ok := got.PriorArtifacts["developing"]
	if !ok || !contains(string(dev), "did it") {
		t.Fatalf("prior_artifacts missing developing: %v", got.PriorArtifacts)
	}
}

func TestEngineLoopBackIncrementsAttempt(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	fp := &fakeProducer{}
	e := NewEngine(s, fp)
	_ = e.StartRun(ctx, "run-at", "spec", nil)
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-at", Stage: StageAnalyzing})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-at", Stage: StageViability, ConfirmedComplexity: ComplexityTrivial})

	// First developing dispatch is attempt 1.
	_, payload := decodeStage(t, fp.last())
	if a := attemptOf(t, payload); a != 1 {
		t.Fatalf("initial developing attempt = %d, want 1", a)
	}

	// developing completes, qa fails → loop back to developing at attempt 2.
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-at", Stage: StageDeveloping})
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-at", Stage: StageQA, QAPassed: false})
	typ, payload := decodeStage(t, fp.last())
	if typ != "stage.requested" || !contains(payload, "developer") {
		t.Fatalf("expected redeveloped developing request, got %s / %s", typ, payload)
	}
	if a := attemptOf(t, payload); a != 2 {
		t.Fatalf("loop-back developing attempt = %d, want 2", a)
	}

	// developing completes again → the following qa dispatch is also attempt 2.
	_ = e.OnStageCompleted(ctx, StageCompleted{RunID: "run-at", Stage: StageDeveloping})
	_, payload = decodeStage(t, fp.last())
	if !contains(payload, "\"stage\":\"qa\"") {
		t.Fatalf("expected qa request, got %s", payload)
	}
	if a := attemptOf(t, payload); a != 2 {
		t.Fatalf("post-loop qa attempt = %d, want 2", a)
	}
}

func attemptOf(t *testing.T, payload string) int {
	t.Helper()
	var p struct {
		Attempt int `json:"attempt"`
	}
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatalf("unmarshal attempt: %v", err)
	}
	return p.Attempt
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
