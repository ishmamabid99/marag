package registry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fin/multi-agent-rag/internal/models"
)

func sampleRepo(id string) models.Repo {
	return models.Repo{
		ID:             id,
		Name:           "acme/" + id,
		Provider:       models.GitProviderGitHub,
		RemoteURL:      "https://github.com/acme/" + id,
		DefaultBranch:  "main",
		EmbeddingModel: "nomic-embed-text",
		EmbeddingDim:   768,
		Labels:         map[string]string{"team": "acme"},
	}
}

// storeContract exercises any Store implementation. Reused by the Postgres test.
func storeContract(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()

	if err := s.Create(ctx, sampleRepo("r1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Create(ctx, sampleRepo("r1")); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate Create = %v, want ErrAlreadyExists", err)
	}

	got, err := s.Get(ctx, "r1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "acme/r1" {
		t.Fatalf("Get name = %q", got.Name)
	}

	if _, err := s.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing = %v, want ErrNotFound", err)
	}

	// Heartbeat updates timestamp.
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.Heartbeat(ctx, "r1", now); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	got, _ = s.Get(ctx, "r1")
	if got.LastHeartbeatAt == nil || !got.LastHeartbeatAt.Equal(now) {
		t.Fatalf("Heartbeat not persisted: %v", got.LastHeartbeatAt)
	}

	// MarkIngested clears staleness and sets active version.
	if err := s.MarkIngested(ctx, "r1", now, 0, 2); err != nil {
		t.Fatalf("MarkIngested: %v", err)
	}
	got, _ = s.Get(ctx, "r1")
	if got.IsStale() {
		t.Fatal("repo should not be stale after ingest")
	}
	if got.ActiveVersion != 2 {
		t.Fatalf("ActiveVersion = %d, want 2", got.ActiveVersion)
	}

	// List with provider filter.
	other := sampleRepo("r2")
	other.Provider = models.GitProviderGitLab
	if err := s.Create(ctx, other); err != nil {
		t.Fatalf("Create r2: %v", err)
	}
	gh, err := s.List(ctx, ListFilter{Provider: models.GitProviderGitHub})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(gh) != 1 || gh[0].ID != "r1" {
		t.Fatalf("provider filter returned %d repos", len(gh))
	}

	// Update changes a mutable field and preserves CreatedAt.
	before, err := s.Get(ctx, "r1")
	if err != nil {
		t.Fatalf("Get before update: %v", err)
	}
	toUpdate := before
	toUpdate.Name = "acme/r1-renamed"
	if err := s.Update(ctx, toUpdate); err != nil {
		t.Fatalf("Update: %v", err)
	}
	after, err := s.Get(ctx, "r1")
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if after.Name != "acme/r1-renamed" {
		t.Fatalf("Update did not persist Name, got %q", after.Name)
	}
	if !after.CreatedAt.Equal(before.CreatedAt) {
		t.Fatalf("Update changed CreatedAt: before=%v after=%v", before.CreatedAt, after.CreatedAt)
	}

	// Update on a non-existent repo returns ErrNotFound.
	if err := s.Update(ctx, sampleRepo("missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update missing = %v, want ErrNotFound", err)
	}

	// Get returns an isolated copy: mutating the returned repo's Labels map
	// must not affect the store's internal state (Finding 1).
	iso, err := s.Get(ctx, "r1")
	if err != nil {
		t.Fatalf("Get for isolation check: %v", err)
	}
	iso.Labels["team"] = "mutated"
	iso2, err := s.Get(ctx, "r1")
	if err != nil {
		t.Fatalf("Get after mutation: %v", err)
	}
	if iso2.Labels["team"] != "acme" {
		t.Fatalf("store state mutated via aliased Labels map: got %q, want %q", iso2.Labels["team"], "acme")
	}

	// Delete.
	if err := s.Delete(ctx, "r1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, "r1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, "r1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete missing = %v, want ErrNotFound", err)
	}
}

func TestMemStore(t *testing.T) {
	storeContract(t, NewMemStore())
}
