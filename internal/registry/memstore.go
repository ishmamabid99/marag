package registry

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/fin/multi-agent-rag/internal/models"
)

// MemStore is an in-memory Store for unit tests and local runs.
type MemStore struct {
	mu    sync.RWMutex
	repos map[string]models.Repo
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{repos: make(map[string]models.Repo)}
}

// cloneRepo returns a copy of r with its own Labels map and its own
// LastIngestedAt/LastHeartbeatAt time pointers, so the clone shares no
// mutable state with r. Used at store ingress/egress to prevent callers
// from aliasing internal state guarded by MemStore's mutex.
func cloneRepo(r models.Repo) models.Repo {
	if r.Labels != nil {
		labels := make(map[string]string, len(r.Labels))
		for k, v := range r.Labels {
			labels[k] = v
		}
		r.Labels = labels
	}
	if r.LastIngestedAt != nil {
		t := *r.LastIngestedAt
		r.LastIngestedAt = &t
	}
	if r.LastHeartbeatAt != nil {
		t := *r.LastHeartbeatAt
		r.LastHeartbeatAt = &t
	}
	return r
}

func (m *MemStore) Create(_ context.Context, r models.Repo) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.repos[r.ID]; ok {
		return ErrAlreadyExists
	}
	now := time.Now().UTC()
	r.CreatedAt, r.UpdatedAt = now, now
	m.repos[r.ID] = cloneRepo(r)
	return nil
}

func (m *MemStore) Get(_ context.Context, id string) (models.Repo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.repos[id]
	if !ok {
		return models.Repo{}, ErrNotFound
	}
	return cloneRepo(r), nil
}

func (m *MemStore) Update(_ context.Context, r models.Repo) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.repos[r.ID]
	if !ok {
		return ErrNotFound
	}
	r.CreatedAt = existing.CreatedAt
	r.UpdatedAt = time.Now().UTC()
	m.repos[r.ID] = cloneRepo(r)
	return nil
}

func (m *MemStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.repos[id]; !ok {
		return ErrNotFound
	}
	delete(m.repos, id)
	return nil
}

func (m *MemStore) List(_ context.Context, f ListFilter) ([]models.Repo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []models.Repo
	for _, r := range m.repos {
		if f.matches(r) {
			out = append(out, cloneRepo(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *MemStore) Heartbeat(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.repos[id]
	if !ok {
		return ErrNotFound
	}
	t := at.UTC()
	r.LastHeartbeatAt = &t
	r.UpdatedAt = time.Now().UTC()
	m.repos[id] = r
	return nil
}

func (m *MemStore) MarkIngested(_ context.Context, id string, at time.Time, commitsBehind, activeVersion int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.repos[id]
	if !ok {
		return ErrNotFound
	}
	t := at.UTC()
	r.LastIngestedAt = &t
	r.CommitsBehind = commitsBehind
	r.ActiveVersion = activeVersion
	r.UpdatedAt = time.Now().UTC()
	m.repos[id] = r
	return nil
}
