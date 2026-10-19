package linear

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func newRunStore(t *testing.T) *PGRunStore {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "pgvector/pgvector:pg17",
		postgres.WithDatabase("lin"), postgres.WithUsername("test"), postgres.WithPassword("test"),
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
	s, err := NewPGRunStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPGRunStore: %v", err)
	}
	return s
}

func TestPGRunStoreSaveAndLookup(t *testing.T) {
	ctx := context.Background()
	s := newRunStore(t)
	if err := s.SaveMapping(ctx, "run-1", "iss-9"); err != nil {
		t.Fatalf("SaveMapping: %v", err)
	}
	// Idempotent upsert.
	if err := s.SaveMapping(ctx, "run-1", "iss-9"); err != nil {
		t.Fatalf("SaveMapping (repeat): %v", err)
	}
	issue, ok, err := s.IssueForRun(ctx, "run-1")
	if err != nil || !ok || issue != "iss-9" {
		t.Fatalf("IssueForRun = %q,%v,%v", issue, ok, err)
	}
	if _, ok, _ := s.IssueForRun(ctx, "missing"); ok {
		t.Fatalf("expected not found for missing run")
	}
}

func TestRunForIssue(t *testing.T) {
	ctx := context.Background()
	s := newRunStore(t)
	if _, ok, err := s.RunForIssue(ctx, "iss-x"); err != nil || ok {
		t.Fatalf("no mapping: ok=%v err=%v", ok, err)
	}
	if err := s.SaveMapping(ctx, "run-1", "iss-x"); err != nil {
		t.Fatalf("save run-1: %v", err)
	}
	if err := s.SaveMapping(ctx, "run-2", "iss-x"); err != nil {
		t.Fatalf("save run-2: %v", err)
	}
	got, ok, err := s.RunForIssue(ctx, "iss-x")
	if err != nil || !ok {
		t.Fatalf("lookup: ok=%v err=%v", ok, err)
	}
	if got != "run-2" {
		t.Fatalf("RunForIssue=%q want run-2 (most recent)", got)
	}
}
