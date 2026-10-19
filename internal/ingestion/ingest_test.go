package ingestion

import (
	"context"
	"errors"
	"testing"

	"github.com/fin/multi-agent-rag/pkg/embedder"
)

// Noise files (dependency locks, minified bundles) must never reach the index,
// even when handed directly to Ingest — the exclusion is central, not just in
// the CLI walk.
func TestIngestExcludesNoiseFiles(t *testing.T) {
	ctx := context.Background()
	pool, svc := newTestDB(t, "ingnoise")
	ing := NewIngestor(pool, svc, NewVectorWriter(pool), embedder.NewFake(768))

	files := map[string]string{
		"pkg/models/book.go": "package models\ntype Book struct{ Name string }\n",
		"go.sum":             "github.com/x/y v1.0.0 h1:deadbeef=\ngithub.com/x/y v1.0.0/go.mod h1:cafe=\n",
		"web/app.min.js":     "!function(){var a=1}();\n",
		"yarn.lock":          "# yarn lockfile v1\nfoo@^1.0.0:\n  version \"1.0.0\"\n",
		"config.local.json":  "{\"anthropic\":{\"api_key\":\"sk-ant-REALLOOKINGSECRET000\"}}\n", // must NEVER be indexed
	}
	if _, err := ing.Ingest(ctx, "ingnoise", files, 0); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	rows, err := pool.Query(ctx, "select distinct file_path from code_embeddings where repo_id='ingnoise'")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen[fp] = true
	}
	if !seen["pkg/models/book.go"] {
		t.Fatal("real code file book.go should be indexed")
	}
	for _, noise := range []string{"go.sum", "web/app.min.js", "yarn.lock", "config.local.json"} {
		if seen[noise] {
			t.Errorf("excluded file %q must NOT be indexed; indexed paths=%v", noise, seen)
		}
	}
}

func TestIngestFlipsActiveVersion(t *testing.T) {
	ctx := context.Background()
	pool, svc := newTestDB(t, "ing1")
	ing := NewIngestor(pool, svc, NewVectorWriter(pool), embedder.NewFake(768))

	files := map[string]string{
		"a.go": "package a\nfunc A() {}\n",
		"b.go": "package a\nfunc B() {}\n",
	}
	res, err := ing.Ingest(ctx, "ing1", files, 0)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if res.Version != 1 {
		t.Fatalf("version = %d, want 1", res.Version)
	}
	if res.ChunksWritten < 2 {
		t.Fatalf("chunks written = %d, want >= 2", res.ChunksWritten)
	}

	// Registry active_version flipped, repo no longer stale.
	repo, _ := svc.Get(ctx, "ing1")
	if repo.ActiveVersion != 1 {
		t.Fatalf("active_version = %d, want 1", repo.ActiveVersion)
	}
	if repo.IsStale() {
		t.Fatal("repo should not be stale after ingest")
	}

	// Re-ingest → version 2, and version 1 rows deleted.
	res2, err := ing.Ingest(ctx, "ing1", files, 0)
	if err != nil {
		t.Fatalf("re-Ingest: %v", err)
	}
	if res2.Version != 2 {
		t.Fatalf("version = %d, want 2", res2.Version)
	}
	if res2.OldVersionDeleted == 0 {
		t.Fatal("expected old version rows to be deleted")
	}
	var v1 int
	_ = pool.QueryRow(ctx,
		`SELECT count(*) FROM code_embeddings WHERE repo_id=$1 AND version=1`, "ing1").Scan(&v1)
	if v1 != 0 {
		t.Fatalf("version 1 rows remain: %d", v1)
	}
}

func TestIngestDimensionMismatch(t *testing.T) {
	ctx := context.Background()
	pool, svc := newTestDB(t, "ing2")
	// Repo pinned to 768, embedder produces 128 → mismatch.
	ing := NewIngestor(pool, svc, NewVectorWriter(pool), embedder.NewFake(128))
	_, err := ing.Ingest(ctx, "ing2", map[string]string{"a.go": "x\ny\n"}, 0)
	if !errors.Is(err, embedder.ErrDimensionMismatch) {
		t.Fatalf("err = %v, want ErrDimensionMismatch", err)
	}
}

func TestIngestConcurrentBlocked(t *testing.T) {
	ctx := context.Background()
	pool, svc := newTestDB(t, "ing3")
	ing := NewIngestor(pool, svc, NewVectorWriter(pool), embedder.NewFake(768))

	// Hold the advisory lock manually on a dedicated connection.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	key := advisoryKey("ing3")
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&locked); err != nil {
		t.Fatalf("lock: %v", err)
	}
	if !locked {
		t.Fatal("could not take advisory lock in test setup")
	}
	defer conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, key)

	_, err = ing.Ingest(ctx, "ing3", map[string]string{"a.go": "x\ny\n"}, 0)
	if !errors.Is(err, ErrIngestionInProgress) {
		t.Fatalf("err = %v, want ErrIngestionInProgress", err)
	}
}

func TestIngestRetryNoDuplicateRows(t *testing.T) {
	ctx := context.Background()
	pool, svc := newTestDB(t, "ing4")
	w := NewVectorWriter(pool)
	ing := NewIngestor(pool, svc, w, embedder.NewFake(768))

	files := map[string]string{
		"a.go": "package a\nfunc A() {}\n",
		"b.go": "package a\nfunc B() {}\n",
	}
	if _, err := ing.Ingest(ctx, "ing4", files, 0); err != nil {
		t.Fatalf("initial Ingest: %v", err)
	}

	// Simulate a crashed prior attempt: rows were written at the next
	// version (2), but the crash happened before MarkIngested advanced
	// active_version, leaving them orphaned.
	if _, err := w.WriteChunks(ctx, "ing4", 2, []EmbeddedChunk{embedded("orphan.go", 0, 768)}); err != nil {
		t.Fatalf("seed orphan rows: %v", err)
	}

	// Re-ingest the same files. It recomputes newVersion=2 and must clear
	// the orphaned row before writing, not accumulate on top of it.
	res2, err := ing.Ingest(ctx, "ing4", files, 0)
	if err != nil {
		t.Fatalf("retry Ingest: %v", err)
	}
	if res2.Version != 2 {
		t.Fatalf("version = %d, want 2", res2.Version)
	}

	var v2 int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM code_embeddings WHERE repo_id=$1 AND version=2`, "ing4").Scan(&v2); err != nil {
		t.Fatalf("count: %v", err)
	}
	if v2 != res2.ChunksWritten {
		t.Fatalf("version 2 rows = %d, want %d (orphan should have been cleared, not accumulated)", v2, res2.ChunksWritten)
	}

	repo, _ := svc.Get(ctx, "ing4")
	if repo.ActiveVersion != 2 {
		t.Fatalf("active_version = %d, want 2", repo.ActiveVersion)
	}
}

func TestIngestEmptyDoesNotWipeActive(t *testing.T) {
	ctx := context.Background()
	pool, svc := newTestDB(t, "ing5")
	ing := NewIngestor(pool, svc, NewVectorWriter(pool), embedder.NewFake(768))

	files := map[string]string{
		"a.go": "package a\nfunc A() {}\n",
		"b.go": "package a\nfunc B() {}\n",
	}
	if _, err := ing.Ingest(ctx, "ing5", files, 0); err != nil {
		t.Fatalf("initial Ingest: %v", err)
	}

	var v1Before int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM code_embeddings WHERE repo_id=$1 AND version=1`, "ing5").Scan(&v1Before); err != nil {
		t.Fatalf("count before: %v", err)
	}
	if v1Before == 0 {
		t.Fatal("expected version 1 rows after initial ingest")
	}

	// An empty snapshot must not flip active_version or delete the good copy.
	_, err := ing.Ingest(ctx, "ing5", map[string]string{}, 0)
	if !errors.Is(err, ErrEmptyIngest) {
		t.Fatalf("err = %v, want ErrEmptyIngest", err)
	}

	repo, _ := svc.Get(ctx, "ing5")
	if repo.ActiveVersion != 1 {
		t.Fatalf("active_version = %d, want 1 (unchanged)", repo.ActiveVersion)
	}

	var v1After int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM code_embeddings WHERE repo_id=$1 AND version=1`, "ing5").Scan(&v1After); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if v1After != v1Before {
		t.Fatalf("version 1 rows = %d, want unchanged %d", v1After, v1Before)
	}
}
