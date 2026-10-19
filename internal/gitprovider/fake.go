package gitprovider

import (
	"context"
	"fmt"
	"sync"
)

// Fake is an in-memory GitProvider for tests.
type Fake struct {
	mu              sync.Mutex
	Created         []PRRequest
	Closed          []string
	DeletedBranches []string
	DefaultBranch   string // returned by GetDefaultBranch (default "main")
	n               int
}

// NewFake returns a Fake defaulting to "main" as the default branch.
func NewFake() *Fake { return &Fake{DefaultBranch: "main"} }

func (f *Fake) GetDefaultBranch(_ context.Context, _ RepoRef) (string, error) {
	if f.DefaultBranch == "" {
		return "main", nil
	}
	return f.DefaultBranch, nil
}

func (f *Fake) CreatePR(_ context.Context, _ RepoRef, pr PRRequest) (PRResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	f.Created = append(f.Created, pr)
	return PRResult{ID: fmt.Sprintf("%d", f.n), Number: f.n, URL: fmt.Sprintf("https://example.test/pr/%d", f.n)}, nil
}

func (f *Fake) ClosePR(_ context.Context, _ RepoRef, prID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Closed = append(f.Closed, prID)
	return nil
}

func (f *Fake) DeleteBranch(_ context.Context, _ RepoRef, branch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.DeletedBranches = append(f.DeletedBranches, branch)
	return nil
}
