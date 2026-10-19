package ingestion

import (
	"context"
	"testing"
)

func TestContextStoreUpsertLoadActive(t *testing.T) {
	ctx := context.Background()
	pool, svc := newTestDB(t, "cs1")
	cs := NewContextStore(pool)
	if err := cs.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	// Make version 2 the active one.
	if err := svc.MarkIngested(ctx, "cs1", 0, 2); err != nil {
		t.Fatalf("MarkIngested: %v", err)
	}

	if err := cs.Upsert(ctx, "cs1", 1, "old"); err != nil {
		t.Fatalf("upsert v1: %v", err)
	}
	if err := cs.Upsert(ctx, "cs1", 2, "current"); err != nil {
		t.Fatalf("upsert v2: %v", err)
	}

	got, err := cs.LoadActive(ctx, "cs1")
	if err != nil {
		t.Fatalf("LoadActive: %v", err)
	}
	if got != "current" {
		t.Fatalf("LoadActive=%q want current", got)
	}

	// Upsert overwrites the same version.
	if err := cs.Upsert(ctx, "cs1", 2, "current2"); err != nil {
		t.Fatalf("re-upsert v2: %v", err)
	}
	if got, _ = cs.LoadActive(ctx, "cs1"); got != "current2" {
		t.Fatalf("after re-upsert=%q want current2", got)
	}

	if err := cs.DeleteVersion(ctx, "cs1", 1); err != nil {
		t.Fatalf("DeleteVersion: %v", err)
	}
}

func TestContextStoreLoadActiveEmptyWhenNoCard(t *testing.T) {
	ctx := context.Background()
	pool, _ := newTestDB(t, "cs2")
	cs := NewContextStore(pool)
	if err := cs.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	got, err := cs.LoadActive(ctx, "cs2")
	if err != nil {
		t.Fatalf("LoadActive: %v", err)
	}
	if got != "" {
		t.Fatalf("no card should yield empty string, got %q", got)
	}
}
