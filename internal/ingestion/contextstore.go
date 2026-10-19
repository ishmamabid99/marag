package ingestion

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ContextStore persists per-repo, per-version context cards (raw doc text
// captured at ingest time). It is written under the ingest advisory lock and
// keyed by the same version as the vectors, so retrieval never reads a
// half-updated repo.
type ContextStore struct {
	pool *pgxpool.Pool
}

// NewContextStore returns a store over the given pool.
func NewContextStore(pool *pgxpool.Pool) *ContextStore {
	return &ContextStore{pool: pool}
}

// EnsureSchema creates the repo_context table if it does not exist.
func (s *ContextStore) EnsureSchema(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS repo_context (
		  repo_id    TEXT    NOT NULL,
		  version    INTEGER NOT NULL,
		  card       TEXT    NOT NULL,
		  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		  PRIMARY KEY (repo_id, version)
		)`)
	if err != nil {
		return fmt.Errorf("context store: ensure schema: %w", err)
	}
	return nil
}

// Upsert writes (or overwrites) the card for (repoID, version).
func (s *ContextStore) Upsert(ctx context.Context, repoID string, version int, card string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO repo_context (repo_id, version, card)
		VALUES ($1,$2,$3)
		ON CONFLICT (repo_id, version) DO UPDATE SET card=excluded.card, updated_at=now()`,
		repoID, version, card)
	if err != nil {
		return fmt.Errorf("context store: upsert %s v%d: %w", repoID, version, err)
	}
	return nil
}

// LoadActive returns the card for the repo's active_version, or "" if none.
func (s *ContextStore) LoadActive(ctx context.Context, repoID string) (string, error) {
	var card string
	err := s.pool.QueryRow(ctx, `
		SELECT rc.card FROM repo_context rc
		JOIN repos r ON r.id = rc.repo_id AND r.active_version = rc.version
		WHERE rc.repo_id = $1`, repoID).Scan(&card)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("context store: load active %s: %w", repoID, err)
	}
	return card, nil
}

// DeleteVersion removes the card for a specific version.
func (s *ContextStore) DeleteVersion(ctx context.Context, repoID string, version int) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM repo_context WHERE repo_id=$1 AND version=$2`, repoID, version)
	if err != nil {
		return fmt.Errorf("context store: delete %s v%d: %w", repoID, version, err)
	}
	return nil
}
