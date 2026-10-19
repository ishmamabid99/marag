package worker

import (
	"context"
	"testing"
	"time"

	"github.com/fin/multi-agent-rag/pkg/llm"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

func newPool_pg(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "pgvector/pgvector:pg17",
		postgres.WithDatabase("worker"), postgres.WithUsername("test"), postgres.WithPassword("test"),
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
	return pool
}

func TestCheckpointSaveLoadUpsert(t *testing.T) {
	ctx := context.Background()
	s, err := NewCheckpointStore(ctx, newPool_pg(t))
	if err != nil {
		t.Fatalf("NewCheckpointStore: %v", err)
	}

	if _, ok, err := s.Load(ctx, "run-1", "analyzing", 1); err != nil || ok {
		t.Fatalf("Load absent = ok %v err %v", ok, err)
	}

	cp := Checkpoint{RunID: "run-1", Stage: "analyzing", Attempt: 1, Status: "completed",
		ArtifactJSON: []byte(`{"complexity":"standard"}`), Usage: llm.Usage{InputTokens: 10, OutputTokens: 5}}
	if err := s.Save(ctx, cp); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, ok, err := s.Load(ctx, "run-1", "analyzing", 1)
	if err != nil || !ok {
		t.Fatalf("Load = ok %v err %v", ok, err)
	}
	if got.Status != "completed" || string(got.ArtifactJSON) != `{"complexity": "standard"}` || got.Usage.OutputTokens != 5 {
		t.Fatalf("loaded = %+v", got)
	}

	// Upsert: same key, new status + reason.
	cp.Status = "failed"
	cp.Reason = "handler exploded"
	if err := s.Save(ctx, cp); err != nil {
		t.Fatalf("re-Save: %v", err)
	}
	got, _, _ = s.Load(ctx, "run-1", "analyzing", 1)
	if got.Status != "failed" {
		t.Fatalf("upsert status = %s, want failed", got.Status)
	}
	if got.Reason != "handler exploded" {
		t.Fatalf("upsert reason = %q, want %q", got.Reason, "handler exploded")
	}
}
