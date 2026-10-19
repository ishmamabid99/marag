package models

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// GitProvider identifies a git hosting backend. Values are stable wire strings.
type GitProvider string

const (
	GitProviderGitHub GitProvider = "github"
	GitProviderGitLab GitProvider = "gitlab"
)

// DefaultEmbeddingDim matches nomic-embed / bge default output dimension.
const DefaultEmbeddingDim = 768

// ErrInvalidRepo wraps every validation failure returned by Repo.Validate.
var ErrInvalidRepo = errors.New("invalid repo")

// Valid reports whether p is a supported provider.
func (p GitProvider) Valid() bool {
	switch p {
	case GitProviderGitHub, GitProviderGitLab:
		return true
	default:
		return false
	}
}

// Repo is a repository the platform operates on. It is the registry's core
// aggregate: git-hosting identity plus the pinned embedding model and the
// currently active vector version used by retrieval.
type Repo struct {
	ID              string
	Name            string // human-facing "org/name"
	Provider        GitProvider
	RemoteURL       string
	DefaultBranch   string
	EmbeddingModel  string // pinned model name; changing it requires re-ingest
	EmbeddingDim    int    // vector dimension for this repo's partition
	ActiveVersion   int    // vector version served by retrieval (atomic swap target)
	Labels          map[string]string
	LastIngestedAt  *time.Time
	LastHeartbeatAt *time.Time
	CommitsBehind   int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// IsStale reports whether the repo's vectors are behind its default branch.
// A never-ingested repo is stale by definition.
func (r Repo) IsStale() bool {
	return r.LastIngestedAt == nil || r.CommitsBehind > 0
}

// PartitionName returns the deterministic, SQL-safe name of this repo's
// code_embeddings LIST partition. Repo IDs may contain characters illegal in
// an unquoted identifier, so we hash the ID into a stable hex suffix.
func (r Repo) PartitionName() string {
	sum := sha1.Sum([]byte(r.ID))
	return "code_embeddings_" + hex.EncodeToString(sum[:8])
}

// Validate checks required fields. It does not touch the database.
func (r Repo) Validate() error {
	switch {
	case r.ID == "":
		return fmt.Errorf("validate repo: %w: id is required", ErrInvalidRepo)
	case r.Name == "":
		return fmt.Errorf("validate repo: %w: name is required", ErrInvalidRepo)
	case !r.Provider.Valid():
		return fmt.Errorf("validate repo: %w: invalid provider %q", ErrInvalidRepo, r.Provider)
	case r.RemoteURL == "":
		return fmt.Errorf("validate repo: %w: remote_url is required", ErrInvalidRepo)
	case r.DefaultBranch == "":
		return fmt.Errorf("validate repo: %w: default_branch is required", ErrInvalidRepo)
	case r.EmbeddingModel == "":
		return fmt.Errorf("validate repo: %w: embedding_model is required", ErrInvalidRepo)
	case r.EmbeddingDim <= 0:
		return fmt.Errorf("validate repo: %w: embedding_dim must be positive, got %d", ErrInvalidRepo, r.EmbeddingDim)
	}
	return nil
}
