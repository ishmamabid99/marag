package registry

import (
	"context"
	"errors"
	"time"

	"github.com/fin/multi-agent-rag/internal/models"
)

var (
	// ErrNotFound is returned when a repo id is not registered.
	ErrNotFound = errors.New("registry: repo not found")
	// ErrAlreadyExists is returned when creating a repo whose id is taken.
	ErrAlreadyExists = errors.New("registry: repo already exists")
)

// ListFilter narrows a List query. Zero-value fields are ignored.
type ListFilter struct {
	Provider  models.GitProvider
	Labels    map[string]string // repo must contain all of these label pairs
	StaleOnly bool
}

// matches reports whether r satisfies the filter.
func (f ListFilter) matches(r models.Repo) bool {
	if f.Provider != "" && r.Provider != f.Provider {
		return false
	}
	if f.StaleOnly && !r.IsStale() {
		return false
	}
	for k, v := range f.Labels {
		if r.Labels[k] != v {
			return false
		}
	}
	return true
}

// Store is the persistence boundary for repo registrations.
type Store interface {
	Create(ctx context.Context, r models.Repo) error
	Get(ctx context.Context, id string) (models.Repo, error)
	Update(ctx context.Context, r models.Repo) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context, f ListFilter) ([]models.Repo, error)
	Heartbeat(ctx context.Context, id string, at time.Time) error
	MarkIngested(ctx context.Context, id string, at time.Time, commitsBehind, activeVersion int) error
}
