package worker

import (
	"context"
	"errors"
	"fmt"

	_ "embed"

	"github.com/fin/multi-agent-rag/pkg/llm"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var checkpointSchema string

// Checkpoint records a stage attempt for idempotency and resume.
type Checkpoint struct {
	RunID        string
	Stage        string
	Attempt      int
	Status       string // "completed" | "failed"
	Reason       string // failure reason, set when Status == "failed"
	ArtifactJSON []byte
	Usage        llm.Usage
}

// CheckpointStore persists stage checkpoints in Postgres.
type CheckpointStore struct {
	pool *pgxpool.Pool
}

// NewCheckpointStore applies the schema and returns a store.
func NewCheckpointStore(ctx context.Context, pool *pgxpool.Pool) (*CheckpointStore, error) {
	if _, err := pool.Exec(ctx, checkpointSchema); err != nil {
		return nil, fmt.Errorf("apply checkpoint schema: %w", err)
	}
	return &CheckpointStore{pool: pool}, nil
}

// Save upserts a checkpoint keyed by (run_id, stage, attempt).
func (s *CheckpointStore) Save(ctx context.Context, cp Checkpoint) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO checkpoints (run_id, stage, attempt, status, reason, artifact, input_tokens, output_tokens)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		 ON CONFLICT (run_id, stage, attempt) DO UPDATE
		 SET status=EXCLUDED.status, reason=EXCLUDED.reason, artifact=EXCLUDED.artifact,
		     input_tokens=EXCLUDED.input_tokens, output_tokens=EXCLUDED.output_tokens,
		     updated_at=now()`,
		cp.RunID, cp.Stage, cp.Attempt, cp.Status, cp.Reason, cp.ArtifactJSON, cp.Usage.InputTokens, cp.Usage.OutputTokens)
	if err != nil {
		return fmt.Errorf("save checkpoint: %w", err)
	}
	return nil
}

// Load returns the checkpoint for a stage attempt, ok=false if none exists.
func (s *CheckpointStore) Load(ctx context.Context, runID, stage string, attempt int) (Checkpoint, bool, error) {
	var cp Checkpoint
	err := s.pool.QueryRow(ctx,
		`SELECT run_id, stage, attempt, status, COALESCE(reason,''), artifact, input_tokens, output_tokens
		 FROM checkpoints WHERE run_id=$1 AND stage=$2 AND attempt=$3`,
		runID, stage, attempt).
		Scan(&cp.RunID, &cp.Stage, &cp.Attempt, &cp.Status, &cp.Reason, &cp.ArtifactJSON, &cp.Usage.InputTokens, &cp.Usage.OutputTokens)
	if errors.Is(err, pgx.ErrNoRows) {
		return Checkpoint{}, false, nil
	}
	if err != nil {
		return Checkpoint{}, false, fmt.Errorf("load checkpoint: %w", err)
	}
	return cp, true, nil
}
