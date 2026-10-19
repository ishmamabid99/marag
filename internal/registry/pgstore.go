package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "embed"

	"github.com/fin/multi-agent-rag/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

// PGStore is a Postgres-backed Store. Vectors live in the same instance
// (pgvector); each repo gets a LIST partition of code_embeddings on Create.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore applies the schema and returns a ready store.
func NewPGStore(ctx context.Context, pool *pgxpool.Pool) (*PGStore, error) {
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &PGStore{pool: pool}, nil
}

const repoColumns = `id, name, provider, remote_url, default_branch,
	embedding_model, embedding_dim, active_version, labels, commits_behind,
	last_ingested_at, last_heartbeat_at, created_at, updated_at`

func scanRepo(row pgx.Row) (models.Repo, error) {
	var r models.Repo
	var provider string
	var labels []byte
	err := row.Scan(&r.ID, &r.Name, &provider, &r.RemoteURL, &r.DefaultBranch,
		&r.EmbeddingModel, &r.EmbeddingDim, &r.ActiveVersion, &labels,
		&r.CommitsBehind, &r.LastIngestedAt, &r.LastHeartbeatAt,
		&r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return models.Repo{}, err
	}
	r.Provider = models.GitProvider(provider)
	if len(labels) > 0 {
		if err := json.Unmarshal(labels, &r.Labels); err != nil {
			return models.Repo{}, fmt.Errorf("decode labels: %w", err)
		}
	}
	return r, nil
}

func (s *PGStore) Create(ctx context.Context, r models.Repo) error {
	labels, err := json.Marshal(labelsOrEmpty(r.Labels))
	if err != nil {
		return fmt.Errorf("encode labels: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	_, err = tx.Exec(ctx, `INSERT INTO repos
		(id, name, provider, remote_url, default_branch, embedding_model,
		 embedding_dim, active_version, labels, commits_behind)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		r.ID, r.Name, string(r.Provider), r.RemoteURL, r.DefaultBranch,
		r.EmbeddingModel, r.EmbeddingDim, r.ActiveVersion, labels, r.CommitsBehind)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyExists
		}
		return fmt.Errorf("insert repo: %w", err)
	}

	// Create the repo's vector partition. Partition/relation names cannot be
	// bound as parameters, and Postgres also rejects a bind parameter for the
	// partition bound value in "FOR VALUES IN (...)" (it must be a literal
	// resolved at DDL parse time, not an execution-time parameter — pgx's
	// Describe step sees zero placeholders there and errors with "mismatched
	// param and argument count" if we try). So we use the model-derived safe
	// identifier for the table name and a manually-quoted SQL literal for the
	// value, rather than a bound parameter.
	part := r.PartitionName()
	_, err = tx.Exec(ctx, fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s PARTITION OF code_embeddings FOR VALUES IN (%s)`,
		part, quoteLiteral(r.ID)))
	if err != nil {
		return fmt.Errorf("create partition %s: %w", part, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s *PGStore) Get(ctx context.Context, id string) (models.Repo, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+repoColumns+` FROM repos WHERE id = $1`, id)
	r, err := scanRepo(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.Repo{}, ErrNotFound
	}
	if err != nil {
		return models.Repo{}, fmt.Errorf("get repo: %w", err)
	}
	return r, nil
}

func (s *PGStore) Update(ctx context.Context, r models.Repo) error {
	labels, err := json.Marshal(labelsOrEmpty(r.Labels))
	if err != nil {
		return fmt.Errorf("encode labels: %w", err)
	}
	ct, err := s.pool.Exec(ctx, `UPDATE repos SET
		name=$2, provider=$3, remote_url=$4, default_branch=$5,
		embedding_model=$6, embedding_dim=$7, active_version=$8, labels=$9,
		commits_behind=$10, updated_at=now() WHERE id=$1`,
		r.ID, r.Name, string(r.Provider), r.RemoteURL, r.DefaultBranch,
		r.EmbeddingModel, r.EmbeddingDim, r.ActiveVersion, labels, r.CommitsBehind)
	if err != nil {
		return fmt.Errorf("update repo: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) Delete(ctx context.Context, id string) error {
	// Look up partition name before deleting the row.
	r, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	ct, err := tx.Exec(ctx, `DELETE FROM repos WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("delete repo: %w", err)
	}
	if ct.RowsAffected() == 0 {
		// Row was removed by a concurrent delete between our up-front Get
		// and this DELETE. Report ErrNotFound rather than dropping the
		// partition out from under whatever raced us.
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, r.PartitionName())); err != nil {
		return fmt.Errorf("drop partition: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s *PGStore) List(ctx context.Context, f ListFilter) ([]models.Repo, error) {
	// Build a dynamic WHERE. Provider + staleness in SQL; labels filtered in Go
	// (jsonb containment) to keep the query simple.
	q := `SELECT ` + repoColumns + ` FROM repos WHERE 1=1`
	args := []any{}
	if f.Provider != "" {
		args = append(args, string(f.Provider))
		q += fmt.Sprintf(" AND provider = $%d", len(args))
	}
	if f.StaleOnly {
		q += " AND (last_ingested_at IS NULL OR commits_behind > 0)"
	}
	q += " ORDER BY id"

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list repos: %w", err)
	}
	defer rows.Close()

	var out []models.Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, fmt.Errorf("scan repo: %w", err)
		}
		if labelsMatch(r.Labels, f.Labels) {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

func (s *PGStore) Heartbeat(ctx context.Context, id string, at time.Time) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE repos SET last_heartbeat_at=$2, updated_at=now() WHERE id=$1`,
		id, at.UTC())
	if err != nil {
		return fmt.Errorf("heartbeat: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PGStore) MarkIngested(ctx context.Context, id string, at time.Time, commitsBehind, activeVersion int) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE repos SET last_ingested_at=$2, commits_behind=$3,
		 active_version=$4, updated_at=now() WHERE id=$1`,
		id, at.UTC(), commitsBehind, activeVersion)
	if err != nil {
		return fmt.Errorf("mark ingested: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// labelsOrEmpty substitutes an empty (non-nil) map for a nil Labels field so
// the jsonb column stores {} rather than JSON null, matching the column
// default and keeping jsonb operators (e.g. containment) well-behaved.
func labelsOrEmpty(labels map[string]string) map[string]string {
	if labels == nil {
		return map[string]string{}
	}
	return labels
}

func labelsMatch(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

// quoteLiteral renders s as a Postgres string literal that is safe
// regardless of the standard_conforming_strings setting. That GUC is
// session-settable to "off", in which case a backslash in a plain '...'
// literal is itself an escape character — a naive quoter that only doubles
// single quotes can be broken out of by an id containing a backslash before
// an escaped quote, and because this DDL executes via pgx's simple protocol
// (zero bind args, so no server-side parse-time literal check protects us)
// a breakout can inject stacked statements into the same transaction. To be
// safe under both settings we always double single quotes, and whenever the
// string contains a backslash we also double every backslash and use the
// PostgreSQL escape-string syntax (a leading E'...') so backslash escapes
// are interpreted deterministically rather than depending on the GUC.
// Mirrors lib/pq.QuoteLiteral (reimplemented here to avoid the dependency).
// Used only for the partition bound value, where Postgres requires a
// literal rather than a bind parameter.
func quoteLiteral(s string) string {
	s = strings.ReplaceAll(s, `'`, `''`)
	if strings.Contains(s, `\`) {
		s = strings.ReplaceAll(s, `\`, `\\`)
		return ` E'` + s + `'`
	}
	return `'` + s + `'`
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return false
}
