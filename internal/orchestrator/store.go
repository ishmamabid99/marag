package orchestrator

import (
	"context"
	"errors"
	"fmt"

	_ "embed"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

var (
	ErrRunNotFound = errors.New("orchestrator: run not found")
	ErrRunExists   = errors.New("orchestrator: run already exists")
)

// Compensation is a registered rollback action for a run stage.
type Compensation struct {
	ID          int64
	RunID       string
	Stage       Stage
	Action      string // "delete_branch" | "close_pr" | "close_mr"
	GitProvider string
	ParamsJSON  string
	Executed    bool
}

// Store is the orchestrator's Postgres persistence.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore applies the schema and returns a ready store.
func NewStore(ctx context.Context, pool *pgxpool.Pool) (*Store, error) {
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{pool: pool}, nil
}

// CreateRun inserts a new run.
func (s *Store) CreateRun(ctx context.Context, r Run) error {
	if r.RepoIDs == nil {
		// repo_ids is NOT NULL in the schema; pgx encodes a nil slice as SQL
		// NULL for a text[] column, so normalize to an empty (non-nil) slice.
		r.RepoIDs = []string{}
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO runs (id, status, complexity, current_stage, spec, repo_ids)
		 VALUES ($1,$2,$3,$4,$5,$6)`,
		r.ID, string(r.Status), string(r.Complexity), string(r.CurrentStage), r.Spec, r.RepoIDs)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrRunExists
		}
		return fmt.Errorf("create run: %w", err)
	}
	return nil
}

// GetRun loads a run by id.
func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	var r Run
	var status, complexity, stage string
	err := s.pool.QueryRow(ctx,
		`SELECT id, status, complexity, current_stage, spec, repo_ids, qa_iterations, failure_reason, created_at, updated_at
		 FROM runs WHERE id=$1`, id).
		Scan(&r.ID, &status, &complexity, &stage, &r.Spec, &r.RepoIDs, &r.QAIterations, &r.FailureReason, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrRunNotFound
	}
	if err != nil {
		return Run{}, fmt.Errorf("get run: %w", err)
	}
	r.Status, r.Complexity, r.CurrentStage = RunStatus(status), Complexity(complexity), Stage(stage)
	return r, nil
}

// ListRuns returns the most recently updated runs, newest-first.
func (s *Store) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, status, complexity, current_stage, spec, repo_ids, qa_iterations, failure_reason, created_at, updated_at
		 FROM runs ORDER BY updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var status, complexity, stage string
		if err := rows.Scan(&r.ID, &status, &complexity, &stage, &r.Spec, &r.RepoIDs, &r.QAIterations, &r.FailureReason, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		r.Status, r.Complexity, r.CurrentStage = RunStatus(status), Complexity(complexity), Stage(stage)
		out = append(out, r)
	}
	return out, rows.Err()
}

// AdvanceRun updates the run's stage+status and appends an audit row in one tx.
func (s *Store) AdvanceRun(ctx context.Context, id string, stage Stage, status RunStatus, note string) error {
	return s.txUpdate(ctx, id, stage, status, note,
		`UPDATE runs SET current_stage=$2, status=$3, updated_at=now() WHERE id=$1`,
		id, string(stage), string(status))
}

// SetComplexity updates the routing complexity (set when viability confirms it).
func (s *Store) SetComplexity(ctx context.Context, id string, c Complexity) error {
	ct, err := s.pool.Exec(ctx, `UPDATE runs SET complexity=$2, updated_at=now() WHERE id=$1`, id, string(c))
	if err != nil {
		return fmt.Errorf("set complexity: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrRunNotFound
	}
	return nil
}

// IncrementQAIterations atomically increments the run's QA retry counter and
// returns the new value. Used to bound the developer⇄QA loop.
func (s *Store) IncrementQAIterations(ctx context.Context, id string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`UPDATE runs SET qa_iterations = qa_iterations + 1, updated_at=now() WHERE id=$1 RETURNING qa_iterations`, id).
		Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrRunNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("increment qa iterations: %w", err)
	}
	return n, nil
}

// IncrementQAAndRoute atomically increments qa_iterations AND applies the next
// state in ONE transaction, so a crash/redelivery can't double-count (the
// increment and the stage advance can never diverge). Below maxIter it routes
// to developing/running; at/above it parks the run gated at qa. Returns the new
// iteration count and whether the run was gated.
func (s *Store) IncrementQAAndRoute(ctx context.Context, id string, maxIter int) (iters int, gated bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	err = tx.QueryRow(ctx,
		`UPDATE runs SET qa_iterations = qa_iterations + 1, updated_at=now() WHERE id=$1 RETURNING qa_iterations`, id).
		Scan(&iters)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, ErrRunNotFound
	}
	if err != nil {
		return 0, false, fmt.Errorf("increment qa: %w", err)
	}

	gated = iters >= maxIter
	var stage Stage
	var status RunStatus
	var note string
	if gated {
		stage, status = StageQA, StatusGatedWaitingHuman
		note = fmt.Sprintf("qa failed after %d iterations", iters)
	} else {
		stage, status = StageDeveloping, StatusRunning
		note = fmt.Sprintf("qa failed (iteration %d); redevelop", iters)
	}
	if _, err = tx.Exec(ctx,
		`UPDATE runs SET current_stage=$2, status=$3, failure_reason=CASE WHEN $4 THEN $5 ELSE failure_reason END, updated_at=now() WHERE id=$1`,
		id, string(stage), string(status), gated, note); err != nil {
		return 0, false, fmt.Errorf("route qa: %w", err)
	}
	if err = auditInsert(ctx, tx, id, string(stage), string(status), note); err != nil {
		return 0, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, false, fmt.Errorf("commit: %w", err)
	}
	return iters, gated, nil
}

// GateRun parks a run for human review: status → gated_waiting_human, keeping
// current_stage at the gate stage. Audited in one transaction.
func (s *Store) GateRun(ctx context.Context, id string, stage Stage, reason string) error {
	return s.txUpdate(ctx, id, stage, StatusGatedWaitingHuman, reason,
		`UPDATE runs SET current_stage=$2, status=$3, updated_at=now() WHERE id=$1`,
		id, string(stage), string(StatusGatedWaitingHuman))
}

// FailRun marks a run failed with a reason and audits it, recording the
// stage that failed (not the status) in the audit row.
func (s *Store) FailRun(ctx context.Context, id string, stage Stage, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	ct, err := tx.Exec(ctx,
		`UPDATE runs SET status=$2, failure_reason=$3, updated_at=now() WHERE id=$1`,
		id, string(StatusFailed), reason)
	if err != nil {
		return fmt.Errorf("fail run: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrRunNotFound
	}
	if err := auditInsert(ctx, tx, id, string(stage), string(StatusFailed), reason); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// txUpdate runs an UPDATE + audit insert in one transaction.
func (s *Store) txUpdate(ctx context.Context, id string, stage Stage, status RunStatus, note, updateSQL string, args ...any) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	ct, err := tx.Exec(ctx, updateSQL, args...)
	if err != nil {
		return fmt.Errorf("update run: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrRunNotFound
	}
	if err := auditInsert(ctx, tx, id, string(stage), string(status), note); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func auditInsert(ctx context.Context, tx pgx.Tx, runID, stage, status, note string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO run_audit_log (run_id, stage, status, note) VALUES ($1,$2,$3,$4)`,
		runID, stage, status, note)
	if err != nil {
		return fmt.Errorf("audit insert: %w", err)
	}
	return nil
}

// ClaimIdempotency inserts key, returning true if newly claimed (first time),
// false if it already existed.
func (s *Store) ClaimIdempotency(ctx context.Context, key string) (bool, error) {
	ct, err := s.pool.Exec(ctx,
		`INSERT INTO idempotency_keys (key) VALUES ($1) ON CONFLICT DO NOTHING`, key)
	if err != nil {
		return false, fmt.Errorf("claim idempotency: %w", err)
	}
	return ct.RowsAffected() == 1, nil
}

// ReleaseIdempotency removes a claim so the keyed operation can be retried
// (used when a delivery attempt fully fails).
func (s *Store) ReleaseIdempotency(ctx context.Context, key string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE key=$1`, key); err != nil {
		return fmt.Errorf("release idempotency: %w", err)
	}
	return nil
}

// RegisterCompensation records a rollback action.
func (s *Store) RegisterCompensation(ctx context.Context, c Compensation) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO compensations (run_id, stage, action, git_provider, params_json)
		 VALUES ($1,$2,$3,$4,$5)`,
		c.RunID, string(c.Stage), c.Action, c.GitProvider, []byte(c.ParamsJSON))
	if err != nil {
		return fmt.Errorf("register compensation: %w", err)
	}
	return nil
}

// PendingCompensations returns unexecuted compensations for a run, newest-first
// (reverse registration order, for reverse-order rollback).
func (s *Store) PendingCompensations(ctx context.Context, runID string) ([]Compensation, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, run_id, stage, action, git_provider, params_json, executed
		 FROM compensations WHERE run_id=$1 AND executed=false ORDER BY id DESC`, runID)
	if err != nil {
		return nil, fmt.Errorf("pending compensations: %w", err)
	}
	defer rows.Close()
	var out []Compensation
	for rows.Next() {
		var c Compensation
		var stage string
		var params []byte
		if err := rows.Scan(&c.ID, &c.RunID, &stage, &c.Action, &c.GitProvider, &params, &c.Executed); err != nil {
			return nil, fmt.Errorf("scan compensation: %w", err)
		}
		c.Stage = Stage(stage)
		c.ParamsJSON = string(params)
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkCompensationExecuted flags a compensation done.
func (s *Store) MarkCompensationExecuted(ctx context.Context, id int64) error {
	ct, err := s.pool.Exec(ctx, `UPDATE compensations SET executed=true WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("mark compensation: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("compensation %d not found", id)
	}
	return nil
}

// SaveArtifact upserts a stage's output artifact for later chaining.
func (s *Store) SaveArtifact(ctx context.Context, runID string, stage Stage, artifact []byte) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO run_artifacts (run_id, stage, artifact) VALUES ($1,$2,$3)
		 ON CONFLICT (run_id, stage) DO UPDATE SET artifact=EXCLUDED.artifact, updated_at=now()`,
		runID, string(stage), artifact)
	if err != nil {
		return fmt.Errorf("save artifact: %w", err)
	}
	return nil
}

// ExpandRepoIDs unions add into the run's repo_ids (deduped, order-preserving)
// and persists the result, returning the merged set. A no-op that returns the
// existing set when add contributes nothing new.
func (s *Store) ExpandRepoIDs(ctx context.Context, id string, add []string) ([]string, error) {
	run, err := s.GetRun(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("expand repo ids: %w", err)
	}
	seen := make(map[string]bool, len(run.RepoIDs))
	for _, r := range run.RepoIDs {
		seen[r] = true
	}
	merged := append([]string{}, run.RepoIDs...)
	for _, r := range add {
		if r != "" && !seen[r] {
			seen[r] = true
			merged = append(merged, r)
		}
	}
	if len(merged) == len(run.RepoIDs) {
		return run.RepoIDs, nil // nothing new
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE runs SET repo_ids=$2, updated_at=now() WHERE id=$1`, id, merged); err != nil {
		return nil, fmt.Errorf("expand repo ids: %w", err)
	}
	return merged, nil
}

// LoadArtifacts returns all persisted stage artifacts for a run, keyed by stage.
func (s *Store) LoadArtifacts(ctx context.Context, runID string) (map[Stage][]byte, error) {
	rows, err := s.pool.Query(ctx, `SELECT stage, artifact FROM run_artifacts WHERE run_id=$1`, runID)
	if err != nil {
		return nil, fmt.Errorf("load artifacts: %w", err)
	}
	defer rows.Close()
	out := map[Stage][]byte{}
	for rows.Next() {
		var stage string
		var artifact []byte
		if err := rows.Scan(&stage, &artifact); err != nil {
			return nil, fmt.Errorf("scan artifact: %w", err)
		}
		out[Stage(stage)] = artifact
	}
	return out, rows.Err()
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return false
}
