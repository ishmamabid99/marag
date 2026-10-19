package registry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fin/multi-agent-rag/internal/models"
)

// Service is the registry's business-logic layer over a Store.
type Service struct {
	store Store
}

// NewService wraps a Store.
func NewService(s Store) *Service { return &Service{store: s} }

// Register validates, applies defaults, and persists a new repo.
func (svc *Service) Register(ctx context.Context, r models.Repo) (models.Repo, error) {
	if r.EmbeddingDim == 0 {
		r.EmbeddingDim = models.DefaultEmbeddingDim
	}
	// v1 pins every repo to a single embedding dimension because the
	// code_embeddings partition column is a fixed vector(768). Accepting a
	// different dim would silently create a partition that rejects inserts
	// at ingest time, so reject the mismatch here instead.
	if r.EmbeddingDim != models.DefaultEmbeddingDim {
		return models.Repo{}, fmt.Errorf("register %s: %w: embedding_dim must be %d in v1, got %d",
			r.ID, models.ErrInvalidRepo, models.DefaultEmbeddingDim, r.EmbeddingDim)
	}
	// Registration records identity + config only, not ingestion state. A
	// newly registered repo has no vectors, so we must not trust
	// client-supplied ingestion state; it is set later via MarkIngested and
	// Heartbeat.
	r.ActiveVersion = 0
	r.CommitsBehind = 0
	r.LastIngestedAt = nil
	r.LastHeartbeatAt = nil
	if err := r.Validate(); err != nil {
		return models.Repo{}, err
	}
	if err := svc.store.Create(ctx, r); err != nil {
		return models.Repo{}, fmt.Errorf("register %s: %w", r.ID, err)
	}
	out, err := svc.store.Get(ctx, r.ID)
	if err != nil {
		return models.Repo{}, fmt.Errorf("register %s: reload after create: %w", r.ID, err)
	}
	return out, nil
}

// Get returns a repo by id.
func (svc *Service) Get(ctx context.Context, id string) (models.Repo, error) {
	return svc.store.Get(ctx, id)
}

// List returns repos matching the filter.
func (svc *Service) List(ctx context.Context, f ListFilter) ([]models.Repo, error) {
	return svc.store.List(ctx, f)
}

// Heartbeat records a liveness ping for self-registered repos.
func (svc *Service) Heartbeat(ctx context.Context, id string) error {
	return svc.store.Heartbeat(ctx, id, time.Now())
}

// MarkIngested records a completed ingestion and flips the active vector version.
func (svc *Service) MarkIngested(ctx context.Context, id string, commitsBehind, activeVersion int) error {
	return svc.store.MarkIngested(ctx, id, time.Now(), commitsBehind, activeVersion)
}

// Deregister removes a repo and its vector partition.
func (svc *Service) Deregister(ctx context.Context, id string) error {
	return svc.store.Delete(ctx, id)
}

// Discover resolves an explicit set of repo ids, skipping ids not registered.
func (svc *Service) Discover(ctx context.Context, repoIDs []string) ([]models.Repo, error) {
	out := make([]models.Repo, 0, len(repoIDs))
	for _, id := range repoIDs {
		r, err := svc.store.Get(ctx, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("discover %s: %w", id, err)
		}
		out = append(out, r)
	}
	return out, nil
}
