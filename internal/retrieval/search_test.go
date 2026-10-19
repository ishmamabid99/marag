package retrieval

import (
	"context"
	"testing"
	"time"

	"github.com/fin/multi-agent-rag/internal/models"
	"github.com/fin/multi-agent-rag/internal/pgvec"
	"github.com/fin/multi-agent-rag/internal/registry"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// newTestDB starts pgvector Postgres, applies the registry schema, registers
// repoID, and returns the pool + service. Shared by search and gateway tests.
func newTestDB(t *testing.T, repoID string) (*pgxpool.Pool, *registry.Service) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "pgvector/pgvector:pg17",
		postgres.WithDatabase("retrieval"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
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

	store, err := registry.NewPGStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPGStore: %v", err)
	}
	svc := registry.NewService(store)
	if _, err := svc.Register(ctx, models.Repo{
		ID: repoID, Name: "acme/" + repoID, Provider: models.GitProviderGitHub,
		RemoteURL: "https://github.com/acme/" + repoID, DefaultBranch: "main",
		EmbeddingModel: "fake-embed", EmbeddingDim: 768,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return pool, svc
}

// insertRow writes one embedding row directly (test helper; avoids depending on
// the ingestion package here).
func insertRow(t *testing.T, pool *pgxpool.Pool, repoID string, version, idx int, content string, vec []float32) {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx,
		`INSERT INTO code_embeddings (repo_id, file_path, chunk_index, content, embedding, version)
		 VALUES ($1,$2,$3,$4,$5::vector,$6)`,
		repoID, "f.go", idx, content, vecLit(vec), version)
	if err != nil {
		t.Fatalf("insert row: %v", err)
	}
}

func unit(dim int, hot int) []float32 {
	v := make([]float32, dim)
	v[hot] = 1
	return v
}

func TestSearcherTopKOrdersByDistance(t *testing.T) {
	ctx := context.Background()
	pool, _ := newTestDB(t, "s1")
	// Three rows with distinct one-hot vectors; query aligns with hot=5.
	insertRow(t, pool, "s1", 1, 0, "far", unit(768, 1))
	insertRow(t, pool, "s1", 1, 1, "near", unit(768, 5))
	insertRow(t, pool, "s1", 1, 2, "mid", unit(768, 3))

	s := newSearcher(pool)
	got, err := s.topK(ctx, "s1", 1, 2, unit(768, 5))
	if err != nil {
		t.Fatalf("topK: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (k=2)", len(got))
	}
	// The exact-match row (hot=5, distance 0) must rank first.
	if got[0].Content != "near" {
		t.Fatalf("first result = %q, want \"near\"", got[0].Content)
	}
	if got[0].Distance > got[1].Distance {
		t.Fatalf("results not ordered by ascending distance: %v", got)
	}
}

func TestSearcherVersionIsolation(t *testing.T) {
	ctx := context.Background()
	pool, _ := newTestDB(t, "s2")
	insertRow(t, pool, "s2", 1, 0, "old-version", unit(768, 5))
	insertRow(t, pool, "s2", 2, 0, "new-version", unit(768, 5))

	got, err := newSearcher(pool).topK(ctx, "s2", 2, 10, unit(768, 5))
	if err != nil {
		t.Fatalf("topK: %v", err)
	}
	if len(got) != 1 || got[0].Content != "new-version" {
		t.Fatalf("version filter failed: %v", got)
	}
}

func vecLit(v []float32) string { return pgvec.Literal(v) }
