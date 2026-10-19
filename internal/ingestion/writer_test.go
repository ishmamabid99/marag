package ingestion

import (
	"context"
	"testing"
	"time"

	"github.com/fin/multi-agent-rag/internal/models"
	"github.com/fin/multi-agent-rag/internal/registry"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// newTestDB starts pgvector Postgres, applies the registry schema (which
// creates code_embeddings), registers repoID (creating its partition), and
// returns the pool. Reused by writer and ingest tests.
func newTestDB(t *testing.T, repoID string) (*pgxpool.Pool, *registry.Service) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "pgvector/pgvector:pg17",
		postgres.WithDatabase("ingest"),
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

func embedded(path string, idx int, dim int) EmbeddedChunk {
	v := make([]float32, dim)
	for i := range v {
		v[i] = 0.01 * float32(i%7)
	}
	return EmbeddedChunk{Chunk: Chunk{FilePath: path, Index: idx, Content: "x"}, Embedding: v}
}

func TestVectorWriterWriteAndCount(t *testing.T) {
	ctx := context.Background()
	pool, _ := newTestDB(t, "wr1")
	w := NewVectorWriter(pool)

	n, err := w.WriteChunks(ctx, "wr1", 1, []EmbeddedChunk{
		embedded("a.go", 0, 768), embedded("a.go", 1, 768),
	})
	if err != nil {
		t.Fatalf("WriteChunks: %v", err)
	}
	if n != 2 {
		t.Fatalf("wrote %d, want 2", n)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM code_embeddings WHERE repo_id=$1 AND version=$2`,
		"wr1", 1).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("db count = %d, want 2", count)
	}
}

func TestWriteChunksPersistsKind(t *testing.T) {
	ctx := context.Background()
	pool, _ := newTestDB(t, "wk1")
	w := NewVectorWriter(pool)
	dim := models.DefaultEmbeddingDim

	doc := embedded("CLAUDE.md", 0, dim)
	doc.Chunk.Kind = KindDoc
	code := embedded("main.go", 0, dim)
	code.Chunk.Kind = KindCode
	if _, err := w.WriteChunks(ctx, "wk1", 1, []EmbeddedChunk{doc, code}); err != nil {
		t.Fatalf("WriteChunks: %v", err)
	}

	var docKind, codeKind string
	if err := pool.QueryRow(ctx,
		`SELECT kind FROM code_embeddings WHERE repo_id='wk1' AND file_path='CLAUDE.md'`).Scan(&docKind); err != nil {
		t.Fatalf("query doc kind: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT kind FROM code_embeddings WHERE repo_id='wk1' AND file_path='main.go'`).Scan(&codeKind); err != nil {
		t.Fatalf("query code kind: %v", err)
	}
	if docKind != "doc" || codeKind != "code" {
		t.Fatalf("kinds: CLAUDE.md=%q main.go=%q want doc/code", docKind, codeKind)
	}

	// A chunk left unclassified (empty Kind) defaults to 'code' at write time.
	if _, err := w.WriteChunks(ctx, "wk1", 2, []EmbeddedChunk{embedded("util.go", 0, dim)}); err != nil {
		t.Fatalf("WriteChunks v2: %v", err)
	}
	var def string
	if err := pool.QueryRow(ctx,
		`SELECT kind FROM code_embeddings WHERE repo_id='wk1' AND file_path='util.go'`).Scan(&def); err != nil {
		t.Fatalf("query default kind: %v", err)
	}
	if def != "code" {
		t.Fatalf("default kind=%q want code", def)
	}
}

func TestVectorWriterEnsureIndex(t *testing.T) {
	ctx := context.Background()
	pool, _ := newTestDB(t, "wr2")
	w := NewVectorWriter(pool)
	if err := w.EnsureHNSWIndex(ctx, "wr2"); err != nil {
		t.Fatalf("EnsureHNSWIndex: %v", err)
	}
	// Idempotent.
	if err := w.EnsureHNSWIndex(ctx, "wr2"); err != nil {
		t.Fatalf("EnsureHNSWIndex (2nd): %v", err)
	}
	// Index exists on the repo's partition.
	part := models.Repo{ID: "wr2"}.PartitionName()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_indexes WHERE tablename=$1 AND indexdef ILIKE '%hnsw%'`,
		part).Scan(&n); err != nil {
		t.Fatalf("pg_indexes: %v", err)
	}
	if n == 0 {
		t.Fatalf("no hnsw index on %s", part)
	}
}

func TestVectorWriterDeleteVersion(t *testing.T) {
	ctx := context.Background()
	pool, _ := newTestDB(t, "wr3")
	w := NewVectorWriter(pool)
	_, _ = w.WriteChunks(ctx, "wr3", 1, []EmbeddedChunk{embedded("a.go", 0, 768)})
	_, _ = w.WriteChunks(ctx, "wr3", 2, []EmbeddedChunk{embedded("a.go", 0, 768), embedded("a.go", 1, 768)})

	del, err := w.DeleteVersion(ctx, "wr3", 1)
	if err != nil {
		t.Fatalf("DeleteVersion: %v", err)
	}
	if del != 1 {
		t.Fatalf("deleted %d, want 1", del)
	}
	var remaining int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM code_embeddings WHERE repo_id=$1`, "wr3").Scan(&remaining)
	if remaining != 2 {
		t.Fatalf("remaining = %d, want 2 (version 2 only)", remaining)
	}
}
