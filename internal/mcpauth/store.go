package mcpauth

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

// PGStore is a Postgres-backed TokenStore.
type PGStore struct{ pool *pgxpool.Pool }

// NewPGStore applies the schema and returns a ready store.
func NewPGStore(ctx context.Context, pool *pgxpool.Pool) (*PGStore, error) {
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		return nil, fmt.Errorf("mcpauth: apply schema: %w", err)
	}
	return &PGStore{pool: pool}, nil
}

// Authenticate resolves a token to a principal or returns ErrUnauthenticated
// (an expired token is denied identically to an unknown one).
func (s *PGStore) Authenticate(ctx context.Context, token string) (*Principal, error) {
	if token == "" {
		return nil, ErrUnauthenticated
	}
	var name, scope string
	err := s.pool.QueryRow(ctx,
		`SELECT name, scope FROM mcp_principals WHERE token_sha256=$1 AND (expires_at IS NULL OR expires_at > now())`,
		HashToken(token)).Scan(&name, &scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnauthenticated
	}
	if err != nil {
		return nil, fmt.Errorf("mcpauth: authenticate: %w", err)
	}
	if scope == "" {
		scope = ScopeFull
	}
	return &Principal{Name: name, Scope: scope}, nil
}

// CreatePrincipal stores a new principal by the hash of its token. scope must be
// ScopeContext or ScopeFull. A nil expiresAt means the token never expires.
func (s *PGStore) CreatePrincipal(ctx context.Context, name, token, scope string, expiresAt *time.Time) error {
	if name == "" || token == "" {
		return fmt.Errorf("mcpauth: name and token required")
	}
	if !ValidScope(scope) {
		return fmt.Errorf("mcpauth: invalid scope %q (want %q or %q)", scope, ScopeContext, ScopeFull)
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO mcp_principals (token_sha256, name, scope, expires_at) VALUES ($1,$2,$3,$4)`,
		HashToken(token), name, scope, expiresAt)
	if err != nil {
		return fmt.Errorf("mcpauth: create principal: %w", err)
	}
	return nil
}

// Revoke deletes a principal by name; returns the number of rows removed.
func (s *PGStore) Revoke(ctx context.Context, name string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM mcp_principals WHERE name=$1`, name)
	if err != nil {
		return 0, fmt.Errorf("mcpauth: revoke: %w", err)
	}
	return tag.RowsAffected(), nil
}

// List returns all principals ordered by creation time (no token material).
func (s *PGStore) List(ctx context.Context) ([]PrincipalInfo, error) {
	rows, err := s.pool.Query(ctx, `SELECT name, scope, created_at, expires_at FROM mcp_principals ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("mcpauth: list: %w", err)
	}
	defer rows.Close()
	var out []PrincipalInfo
	for rows.Next() {
		var pi PrincipalInfo
		if err := rows.Scan(&pi.Name, &pi.Scope, &pi.CreatedAt, &pi.ExpiresAt); err != nil {
			return nil, fmt.Errorf("mcpauth: list scan: %w", err)
		}
		out = append(out, pi)
	}
	return out, rows.Err()
}
