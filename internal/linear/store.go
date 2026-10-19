package linear

import (
	"context"
	_ "embed"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

// RunStore persists the run_id → Linear issue_id mapping for PR-links-back.
type RunStore interface {
	SaveMapping(ctx context.Context, runID, issueID string) error
	IssueForRun(ctx context.Context, runID string) (string, bool, error)
	// RunForIssue returns the most recent run started from the issue (for
	// resolving an in-thread approve/reject to a run). ok=false if none.
	RunForIssue(ctx context.Context, issueID string) (string, bool, error)
}

// PGRunStore is a Postgres RunStore.
type PGRunStore struct{ pool *pgxpool.Pool }

// NewPGRunStore applies the schema and returns a ready store.
func NewPGRunStore(ctx context.Context, pool *pgxpool.Pool) (*PGRunStore, error) {
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return nil, fmt.Errorf("linear: apply schema: %w", err)
	}
	return &PGRunStore{pool: pool}, nil
}

// SaveMapping upserts run_id → issue_id.
func (s *PGRunStore) SaveMapping(ctx context.Context, runID, issueID string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO linear_runs (run_id, issue_id) VALUES ($1,$2)
         ON CONFLICT (run_id) DO UPDATE SET issue_id=EXCLUDED.issue_id`, runID, issueID)
	if err != nil {
		return fmt.Errorf("linear: save mapping: %w", err)
	}
	return nil
}

// IssueForRun returns the issue id for a run (ok=false if unknown).
func (s *PGRunStore) IssueForRun(ctx context.Context, runID string) (string, bool, error) {
	var issue string
	err := s.pool.QueryRow(ctx, `SELECT issue_id FROM linear_runs WHERE run_id=$1`, runID).Scan(&issue)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("linear: issue for run: %w", err)
	}
	return issue, true, nil
}

// RunForIssue returns the most recent run started from issueID (ok=false if none).
func (s *PGRunStore) RunForIssue(ctx context.Context, issueID string) (string, bool, error) {
	var runID string
	err := s.pool.QueryRow(ctx,
		`SELECT run_id FROM linear_runs WHERE issue_id=$1 ORDER BY created_at DESC, run_id DESC LIMIT 1`, issueID).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("linear: run for issue: %w", err)
	}
	return runID, true, nil
}

// FakeRunStore is an in-memory RunStore for tests.
type FakeRunStore struct {
	m     map[string]string
	order []string // run ids in save order, for RunForIssue "most recent"
}

// NewFakeRunStore constructs an empty fake.
func NewFakeRunStore() *FakeRunStore { return &FakeRunStore{m: map[string]string{}} }

func (f *FakeRunStore) SaveMapping(_ context.Context, runID, issueID string) error {
	if _, seen := f.m[runID]; !seen {
		f.order = append(f.order, runID)
	}
	f.m[runID] = issueID
	return nil
}
func (f *FakeRunStore) IssueForRun(_ context.Context, runID string) (string, bool, error) {
	v, ok := f.m[runID]
	return v, ok, nil
}
func (f *FakeRunStore) RunForIssue(_ context.Context, issueID string) (string, bool, error) {
	for i := len(f.order) - 1; i >= 0; i-- {
		if f.m[f.order[i]] == issueID {
			return f.order[i], true, nil
		}
	}
	return "", false, nil
}
