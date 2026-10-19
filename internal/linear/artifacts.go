package linear

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GateArtifactReader fetches a run's stage artifact (JSON) so a gate
// notification can be enriched with that stage's detailed findings.
type GateArtifactReader interface {
	Artifact(ctx context.Context, runID, stage string) ([]byte, bool, error)
}

// PGArtifactReader reads the orchestrator's run_artifacts table (shared DB).
type PGArtifactReader struct{ pool *pgxpool.Pool }

// NewPGArtifactReader returns a reader over the given pool.
func NewPGArtifactReader(pool *pgxpool.Pool) *PGArtifactReader { return &PGArtifactReader{pool: pool} }

// Artifact returns the artifact JSON for (runID, stage); ok=false if none.
func (r *PGArtifactReader) Artifact(ctx context.Context, runID, stage string) ([]byte, bool, error) {
	var a []byte
	err := r.pool.QueryRow(ctx,
		`SELECT artifact FROM run_artifacts WHERE run_id=$1 AND stage=$2`, runID, stage).Scan(&a)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("linear: read artifact %s/%s: %w", runID, stage, err)
	}
	return a, true, nil
}
