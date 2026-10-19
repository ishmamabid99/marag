// Package mcpauth authenticates MCP callers by a per-user token (stored as a
// SHA-256 hash). Authorization scoping (per-repo visibility) is deferred: an
// authenticated principal currently has full access.
package mcpauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// Scope bounds what an authenticated principal may do.
//   - ScopeContext: read-only knowledge-base tools (no Anthropic spend).
//   - ScopeFull: everything, including triggering/approving runs (spends the
//     platform's Anthropic budget).
const (
	ScopeContext = "context"
	ScopeFull    = "full"
)

// ValidScope reports whether s is a recognized scope.
func ValidScope(s string) bool { return s == ScopeContext || s == ScopeFull }

// Principal is an authenticated MCP caller.
type Principal struct {
	Name  string
	Scope string // ScopeContext | ScopeFull
}

// PrincipalInfo is a listing row (no token material).
type PrincipalInfo struct {
	Name      string
	Scope     string
	CreatedAt time.Time
	ExpiresAt *time.Time
}

// ErrUnauthenticated is returned when a token is missing or unknown.
var ErrUnauthenticated = errors.New("mcpauth: unauthenticated")

// TokenStore resolves a token to a principal.
type TokenStore interface {
	Authenticate(ctx context.Context, token string) (*Principal, error)
}

// HashToken returns the hex SHA-256 of a token (what we store/compare).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// GenerateToken returns a new random 32-byte token as a hex string.
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

type fakeEntry struct {
	name      string
	scope     string
	expiresAt *time.Time
}

// FakeStore is an in-memory TokenStore for tests.
type FakeStore struct{ byHash map[string]fakeEntry }

// NewFakeStore constructs an empty fake.
func NewFakeStore() *FakeStore { return &FakeStore{byHash: map[string]fakeEntry{}} }

// Add registers a full-scope token → principal with no expiry.
func (f *FakeStore) Add(token, name string) { f.AddScoped(token, name, ScopeFull, nil) }

// AddWithExpiry registers a full-scope token → principal with an optional expiry.
func (f *FakeStore) AddWithExpiry(token, name string, expiresAt *time.Time) {
	f.AddScoped(token, name, ScopeFull, expiresAt)
}

// AddScoped registers a token → principal with an explicit scope + optional expiry.
func (f *FakeStore) AddScoped(token, name, scope string, expiresAt *time.Time) {
	f.byHash[HashToken(token)] = fakeEntry{name: name, scope: scope, expiresAt: expiresAt}
}

// Authenticate resolves a token or returns ErrUnauthenticated (incl. expired).
func (f *FakeStore) Authenticate(_ context.Context, token string) (*Principal, error) {
	if token == "" {
		return nil, ErrUnauthenticated
	}
	e, ok := f.byHash[HashToken(token)]
	if !ok {
		return nil, ErrUnauthenticated
	}
	if e.expiresAt != nil && time.Now().After(*e.expiresAt) {
		return nil, ErrUnauthenticated
	}
	scope := e.scope
	if scope == "" {
		scope = ScopeFull
	}
	return &Principal{Name: e.name, Scope: scope}, nil
}
