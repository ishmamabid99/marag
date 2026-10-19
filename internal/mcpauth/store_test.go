package mcpauth

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

func newStore(t *testing.T) (*PGStore, context.Context) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "pgvector/pgvector:pg17",
		postgres.WithDatabase("auth"), postgres.WithUsername("test"), postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
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
	s, err := NewPGStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPGStore: %v", err)
	}
	return s, ctx
}

func TestPGStoreLifecycle(t *testing.T) {
	s, ctx := newStore(t)

	// never-expiring token → authenticates.
	if err := s.CreatePrincipal(ctx, "alice", "tok-alice", ScopeFull, nil); err != nil {
		t.Fatalf("create alice: %v", err)
	}
	if p, err := s.Authenticate(ctx, "tok-alice"); err != nil || p.Name != "alice" {
		t.Fatalf("authenticate alice: p=%v err=%v", p, err)
	}

	// already-expired token → denied like unknown.
	past := time.Now().Add(-time.Minute)
	if err := s.CreatePrincipal(ctx, "expired", "tok-expired", ScopeContext, &past); err != nil {
		t.Fatalf("create expired: %v", err)
	}
	if _, err := s.Authenticate(ctx, "tok-expired"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired token must be denied, got %v", err)
	}

	// future expiry → authenticates.
	future := time.Now().Add(time.Hour)
	if err := s.CreatePrincipal(ctx, "future", "tok-future", ScopeFull, &future); err != nil {
		t.Fatalf("create future: %v", err)
	}
	if _, err := s.Authenticate(ctx, "tok-future"); err != nil {
		t.Fatalf("future token should authenticate: %v", err)
	}

	// list shows all three with correct expiry nullability.
	infos, err := s.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(infos) != 3 {
		t.Fatalf("list len = %d, want 3", len(infos))
	}
	byName := map[string]PrincipalInfo{}
	for _, pi := range infos {
		byName[pi.Name] = pi
	}
	if byName["alice"].ExpiresAt != nil {
		t.Fatalf("alice should have no expiry")
	}
	if byName["future"].ExpiresAt == nil {
		t.Fatalf("future should have an expiry")
	}

	// revoke removes and denies.
	n, err := s.Revoke(ctx, "alice")
	if err != nil || n != 1 {
		t.Fatalf("revoke alice: n=%d err=%v", n, err)
	}
	if _, err := s.Authenticate(ctx, "tok-alice"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked token must be denied, got %v", err)
	}

	// revoking an unknown principal removes nothing.
	if n, err := s.Revoke(ctx, "nobody"); err != nil || n != 0 {
		t.Fatalf("revoke unknown: n=%d err=%v", n, err)
	}
}

func TestScopedPrincipalRoundTrip(t *testing.T) {
	s, ctx := newStore(t)
	if err := s.CreatePrincipal(ctx, "reader", "tok-reader", ScopeContext, nil); err != nil {
		t.Fatalf("create context principal: %v", err)
	}
	if err := s.CreatePrincipal(ctx, "operator", "tok-op", ScopeFull, nil); err != nil {
		t.Fatalf("create full principal: %v", err)
	}
	if err := s.CreatePrincipal(ctx, "bad", "tok-bad", "admin", nil); err == nil {
		t.Fatal("invalid scope must be rejected")
	}
	if p, err := s.Authenticate(ctx, "tok-reader"); err != nil || p.Scope != ScopeContext {
		t.Fatalf("reader: p=%v err=%v", p, err)
	}
	if p, err := s.Authenticate(ctx, "tok-op"); err != nil || p.Scope != ScopeFull {
		t.Fatalf("operator: p=%v err=%v", p, err)
	}
	infos, err := s.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var readerScope string
	for _, pi := range infos {
		if pi.Name == "reader" {
			readerScope = pi.Scope
		}
	}
	if readerScope != ScopeContext {
		t.Fatalf("list should surface reader scope=context, got %q", readerScope)
	}
}
