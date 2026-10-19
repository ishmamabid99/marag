package registry

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// startPostgresDSN spins up a pgvector-enabled Postgres and returns its DSN
// (rather than an already-built pool), so callers that need a non-default
// pgxpool.Config (e.g. a custom AfterConnect hook) can build their own pool.
func startPostgresDSN(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "pgvector/pgvector:pg17",
		postgres.WithDatabase("registry"),
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
		t.Fatalf("connection string: %v", err)
	}
	return dsn
}

// startPostgres spins up a pgvector-enabled Postgres and returns a pool.
func startPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := startPostgresDSN(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestPGStoreContract(t *testing.T) {
	pool := startPostgres(t)
	s, err := NewPGStore(context.Background(), pool)
	if err != nil {
		t.Fatalf("NewPGStore: %v", err)
	}
	storeContract(t, s)
}

func TestPGStoreCreatesPartition(t *testing.T) {
	ctx := context.Background()
	pool := startPostgres(t)
	s, err := NewPGStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPGStore: %v", err)
	}
	r := sampleRepo("part-check")
	if err := s.Create(ctx, r); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The partition must now exist as a relation.
	var exists bool
	err = pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = $1)`,
		r.PartitionName()).Scan(&exists)
	if err != nil {
		t.Fatalf("query pg_class: %v", err)
	}
	if !exists {
		t.Fatalf("partition %q was not created", r.PartitionName())
	}
}

// TestPGStoreCreateRejectsInjection guards against a naive quoteLiteral
// implementation. Create embeds r.ID as a literal (not a bind parameter) in
// the partition DDL — via tx.Exec with zero bind arguments, which pgx
// always sends over the *simple* query protocol (see pgx's exec(): "Always
// use simple protocol when there are no arguments"). The simple protocol
// permits multiple semicolon-separated statements in one message, so a
// literal breakout there is a genuine stacked-statement injection, not just
// a malformed query.
//
// The breakout itself depends on standard_conforming_strings: with it on
// (Postgres's default), backslash is an ordinary character and a quoter
// that only doubles single quotes is already safe. The bug only bites when
// a session has standard_conforming_strings=off, where backslash resumes
// its role as the escape-string escape character even inside a plain
// '...' literal, letting `\'` collapse to a single escaped quote that
// closes the literal one character early. To make this test actually
// exercise (and fail under) the vulnerable path rather than passing
// vacuously under the default GUC, it runs against a pool whose sessions
// have standard_conforming_strings explicitly forced off.
func TestPGStoreCreateRejectsInjection(t *testing.T) {
	ctx := context.Background()
	dsn := startPostgresDSN(t)

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SET standard_conforming_strings = off`)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	s, err := NewPGStore(ctx, pool)
	if err != nil {
		t.Fatalf("NewPGStore: %v", err)
	}

	// Crafted to break a naive "double the single quotes" quoter once
	// standard_conforming_strings is off: the backslash escapes the first
	// quote (consuming it), so the very next quote closes the literal early,
	// leaving `); DROP TABLE repos; --` to run as trailing statements.
	const evilID = `evil\'); DROP TABLE repos; --`
	r := sampleRepo(evilID)
	r.Name = "acme/evil-repo"

	if err := s.Create(ctx, r); err != nil {
		t.Fatalf("Create with malicious id: %v", err)
	}

	// The repos table must still exist and be queryable.
	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'repos')`,
	).Scan(&exists); err != nil {
		t.Fatalf("query information_schema: %v", err)
	}
	if !exists {
		t.Fatal("repos table was dropped — SQL injection via quoteLiteral succeeded")
	}

	// The exact id must round-trip through Get, proving the literal made it
	// into the partition bound intact rather than being truncated/broken by
	// a quoting failure.
	got, err := s.Get(ctx, evilID)
	if err != nil {
		t.Fatalf("Get with malicious id: %v", err)
	}
	if got.ID != evilID {
		t.Fatalf("Get id = %q, want %q", got.ID, evilID)
	}

	// The partition for this repo must exist (proves Create's partition DDL
	// actually executed for this id rather than silently failing).
	var partExists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = $1)`,
		r.PartitionName()).Scan(&partExists); err != nil {
		t.Fatalf("query pg_class: %v", err)
	}
	if !partExists {
		t.Fatalf("partition %q was not created", r.PartitionName())
	}
}
