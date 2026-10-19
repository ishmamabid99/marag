package registry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fin/multi-agent-rag/internal/models"
)

func TestServiceRegisterDefaultsDim(t *testing.T) {
	svc := NewService(NewMemStore())
	in := sampleRepo("d1")
	in.EmbeddingDim = 0 // should default
	out, err := svc.Register(context.Background(), in)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if out.EmbeddingDim != models.DefaultEmbeddingDim {
		t.Fatalf("EmbeddingDim = %d, want %d", out.EmbeddingDim, models.DefaultEmbeddingDim)
	}
}

func TestServiceRegisterZerosIngestionState(t *testing.T) {
	svc := NewService(NewMemStore())
	in := sampleRepo("z1")
	in.ActiveVersion = 7
	in.CommitsBehind = 3
	ts := time.Now()
	in.LastIngestedAt = &ts
	out, err := svc.Register(context.Background(), in)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if out.ActiveVersion != 0 {
		t.Fatalf("ActiveVersion = %d, want 0", out.ActiveVersion)
	}
	if out.CommitsBehind != 0 {
		t.Fatalf("CommitsBehind = %d, want 0", out.CommitsBehind)
	}
	if !out.IsStale() {
		t.Fatal("expected newly registered repo to be stale (never ingested)")
	}
}

func TestServiceRegisterRejectsBadDim(t *testing.T) {
	svc := NewService(NewMemStore())
	in := sampleRepo("z2")
	in.EmbeddingDim = 1024
	_, err := svc.Register(context.Background(), in)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, models.ErrInvalidRepo) {
		t.Fatalf("errors.Is(err, models.ErrInvalidRepo) = false, err = %v", err)
	}
}

func TestServiceRegisterRejectsInvalid(t *testing.T) {
	svc := NewService(NewMemStore())
	in := sampleRepo("d2")
	in.Provider = "svn"
	if _, err := svc.Register(context.Background(), in); err == nil {
		t.Fatal("expected validation error, got nil")
	}
}

func TestServiceHeartbeatStamps(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemStore())
	if _, err := svc.Register(ctx, sampleRepo("d3")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := svc.Heartbeat(ctx, "d3"); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	got, _ := svc.Get(ctx, "d3")
	if got.LastHeartbeatAt == nil {
		t.Fatal("heartbeat not stamped")
	}
}

func TestServiceDiscoverSkipsUnknown(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemStore())
	_, _ = svc.Register(ctx, sampleRepo("k1"))
	_, _ = svc.Register(ctx, sampleRepo("k2"))
	got, err := svc.Discover(ctx, []string{"k1", "nope", "k2"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Discover returned %d, want 2", len(got))
	}
	if got[0].ID != "k1" || got[1].ID != "k2" {
		t.Fatalf("Discover returned wrong repos: %v, want [k1 k2]", []string{got[0].ID, got[1].ID})
	}
}

func TestServiceGetMissing(t *testing.T) {
	svc := NewService(NewMemStore())
	if _, err := svc.Get(context.Background(), "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing = %v, want ErrNotFound", err)
	}
}
