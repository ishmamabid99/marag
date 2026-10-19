package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "pgvector/pgvector:pg17",
		postgres.WithDatabase("orch"), postgres.WithUsername("test"), postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)))
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	s, err := NewStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func sampleRun(id string) Run {
	return Run{ID: id, Status: StatusRequested, Complexity: ComplexityComplex,
		CurrentStage: FirstStage, Spec: "add feature", RepoIDs: []string{"repo-a"}}
}

func TestStoreRunLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	if err := s.CreateRun(ctx, sampleRun("r1")); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.CreateRun(ctx, sampleRun("r1")); !errors.Is(err, ErrRunExists) {
		t.Fatalf("duplicate CreateRun = %v, want ErrRunExists", err)
	}

	got, err := s.GetRun(ctx, "r1")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.Status != StatusRequested || got.CurrentStage != FirstStage || len(got.RepoIDs) != 1 {
		t.Fatalf("run = %+v", got)
	}

	if _, err := s.GetRun(ctx, "missing"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("GetRun missing = %v, want ErrRunNotFound", err)
	}

	if err := s.SetComplexity(ctx, "r1", ComplexityTrivial); err != nil {
		t.Fatalf("SetComplexity: %v", err)
	}
	if err := s.AdvanceRun(ctx, "r1", StageViability, StatusRunning, "advanced to viability"); err != nil {
		t.Fatalf("AdvanceRun: %v", err)
	}
	got, _ = s.GetRun(ctx, "r1")
	if got.CurrentStage != StageViability || got.Status != StatusRunning || got.Complexity != ComplexityTrivial {
		t.Fatalf("after advance: %+v", got)
	}

	if err := s.FailRun(ctx, "r1", StageViability, "viability rejected"); err != nil {
		t.Fatalf("FailRun: %v", err)
	}
	got, _ = s.GetRun(ctx, "r1")
	if got.Status != StatusFailed || got.FailureReason == nil || *got.FailureReason != "viability rejected" {
		t.Fatalf("after fail: %+v", got)
	}
}

func TestStoreCreateRunNilRepoIDs(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	run := Run{ID: "r-nil-repos", Status: StatusRequested, Complexity: ComplexityComplex,
		CurrentStage: FirstStage, Spec: "add feature", RepoIDs: nil}
	if err := s.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun with nil RepoIDs: %v", err)
	}

	got, err := s.GetRun(ctx, "r-nil-repos")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if len(got.RepoIDs) != 0 {
		t.Fatalf("RepoIDs = %+v, want empty", got.RepoIDs)
	}
}

func TestStoreIdempotency(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	claimed, err := s.ClaimIdempotency(ctx, "run-1:analyzing:1")
	if err != nil || !claimed {
		t.Fatalf("first claim = %v,%v want true,nil", claimed, err)
	}
	claimed, err = s.ClaimIdempotency(ctx, "run-1:analyzing:1")
	if err != nil || claimed {
		t.Fatalf("second claim = %v,%v want false,nil", claimed, err)
	}
}

func TestStoreGateAndQAIterations(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.CreateRun(ctx, sampleRun("rg")); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	n, err := s.IncrementQAIterations(ctx, "rg")
	if err != nil || n != 1 {
		t.Fatalf("increment 1 = %d,%v want 1,nil", n, err)
	}
	n, err = s.IncrementQAIterations(ctx, "rg")
	if err != nil || n != 2 {
		t.Fatalf("increment 2 = %d,%v want 2,nil", n, err)
	}
	got, _ := s.GetRun(ctx, "rg")
	if got.QAIterations != 2 {
		t.Fatalf("QAIterations = %d, want 2", got.QAIterations)
	}

	if err := s.GateRun(ctx, "rg", StageRisk, "risk recommended gate"); err != nil {
		t.Fatalf("GateRun: %v", err)
	}
	got, _ = s.GetRun(ctx, "rg")
	if got.Status != StatusGatedWaitingHuman || got.CurrentStage != StageRisk {
		t.Fatalf("after gate: status=%s stage=%s", got.Status, got.CurrentStage)
	}

	if _, err := s.IncrementQAIterations(ctx, "missing"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("increment missing = %v, want ErrRunNotFound", err)
	}
}

func TestIncrementQAAndRoute(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.CreateRun(ctx, sampleRun("rr")); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	// Below max → routes to developing/running.
	iters, gated, err := s.IncrementQAAndRoute(ctx, "rr", 3)
	if err != nil || iters != 1 || gated {
		t.Fatalf("route 1 = (%d,%v,%v) want (1,false,nil)", iters, gated, err)
	}
	got, _ := s.GetRun(ctx, "rr")
	if got.CurrentStage != StageDeveloping || got.Status != StatusRunning {
		t.Fatalf("after redevelop: stage=%s status=%s", got.CurrentStage, got.Status)
	}
	// Reaching max → gated at qa.
	_, _, _ = s.IncrementQAAndRoute(ctx, "rr", 3)
	iters, gated, err = s.IncrementQAAndRoute(ctx, "rr", 3)
	if err != nil || iters != 3 || !gated {
		t.Fatalf("route 3 = (%d,%v,%v) want (3,true,nil)", iters, gated, err)
	}
	got, _ = s.GetRun(ctx, "rr")
	if got.CurrentStage != StageQA || got.Status != StatusGatedWaitingHuman {
		t.Fatalf("after gate: stage=%s status=%s", got.CurrentStage, got.Status)
	}
	if _, _, err := s.IncrementQAAndRoute(ctx, "missing", 3); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("missing = %v, want ErrRunNotFound", err)
	}
}

func TestStoreCompensations(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	_ = s.CreateRun(ctx, sampleRun("r2"))
	if err := s.RegisterCompensation(ctx, Compensation{RunID: "r2", Stage: StageDeveloping, Action: "delete_branch", GitProvider: "github", ParamsJSON: `{"branch":"feat/r2"}`}); err != nil {
		t.Fatalf("Register 1: %v", err)
	}
	if err := s.RegisterCompensation(ctx, Compensation{RunID: "r2", Stage: StageDeveloping, Action: "close_pr", GitProvider: "github", ParamsJSON: `{"pr":42}`}); err != nil {
		t.Fatalf("Register 2: %v", err)
	}
	pending, err := s.PendingCompensations(ctx, "r2")
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %d, want 2", len(pending))
	}
	// Reverse order: most recently registered first.
	if pending[0].Action != "close_pr" || pending[1].Action != "delete_branch" {
		t.Fatalf("order = %s,%s want close_pr,delete_branch", pending[0].Action, pending[1].Action)
	}
	if err := s.MarkCompensationExecuted(ctx, pending[0].ID); err != nil {
		t.Fatalf("Mark: %v", err)
	}
	pending, _ = s.PendingCompensations(ctx, "r2")
	if len(pending) != 1 || pending[0].Action != "delete_branch" {
		t.Fatalf("after mark: %+v", pending)
	}
}

func TestStoreListRuns(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	if err := s.CreateRun(ctx, sampleRun("older")); err != nil {
		t.Fatalf("CreateRun older: %v", err)
	}
	if err := s.CreateRun(ctx, sampleRun("newer")); err != nil {
		t.Fatalf("CreateRun newer: %v", err)
	}
	// Force "newer" to have a later updated_at than "older" via a real update.
	if err := s.AdvanceRun(ctx, "newer", StageViability, StatusRunning, "advance"); err != nil {
		t.Fatalf("AdvanceRun: %v", err)
	}

	runs, err := s.ListRuns(ctx, 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) < 2 {
		t.Fatalf("runs = %d, want at least 2", len(runs))
	}
	if runs[0].ID != "newer" || runs[1].ID != "older" {
		t.Fatalf("order = %s,%s want newer,older", runs[0].ID, runs[1].ID)
	}
}

func TestStoreArtifacts(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	_ = s.CreateRun(ctx, sampleRun("ra"))

	if err := s.SaveArtifact(ctx, "ra", StageDeveloping, []byte(`{"tests_passed":true}`)); err != nil {
		t.Fatalf("SaveArtifact: %v", err)
	}
	// Upsert: a second save for the same (run,stage) replaces.
	if err := s.SaveArtifact(ctx, "ra", StageDeveloping, []byte(`{"tests_passed":false}`)); err != nil {
		t.Fatalf("SaveArtifact 2: %v", err)
	}
	if err := s.SaveArtifact(ctx, "ra", StageDesigning, []byte(`{"file_changes":[]}`)); err != nil {
		t.Fatalf("SaveArtifact designing: %v", err)
	}

	arts, err := s.LoadArtifacts(ctx, "ra")
	if err != nil {
		t.Fatalf("LoadArtifacts: %v", err)
	}
	if len(arts) != 2 {
		t.Fatalf("artifacts = %d, want 2", len(arts))
	}
	if string(arts[StageDeveloping]) == "" || !contains(string(arts[StageDeveloping]), "false") {
		t.Fatalf("developing artifact = %s, want upserted", arts[StageDeveloping])
	}
}
