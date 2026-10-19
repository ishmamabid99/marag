// Package managedagent runs the developer stage inside a Managed Agents session.
// FakeMAClient is for tests; HTTPMAClient (added later) uses the Anthropic SDK.
package managedagent

import (
	"context"
	"sync"
)

// MASession is the input for one repo's Managed-Agents developer session.
type MASession struct {
	Spec       string
	Design     []byte
	RepoID     string
	RemoteURL  string // plain remote (MA auths via a Vault credential)
	BaseBranch string
	Branch     string // work branch the session should create + push
	Model      string
}

// MAResult is the structured outcome of an MA developer session.
type MAResult struct {
	Branch       string
	HeadCommit   string
	FilesChanged []string
	TestsPassed  bool
	Summary      string
}

// MAClient runs a developer session on Managed Agents.
type MAClient interface {
	RunDeveloperSession(ctx context.Context, req MASession) (MAResult, error)
}

// FakeMAClient is a deterministic MAClient for tests.
type FakeMAClient struct {
	fn    func(context.Context, MASession) (MAResult, error)
	mu    sync.Mutex
	calls []MASession
}

// NewFakeMAClient wraps a session function.
func NewFakeMAClient(fn func(context.Context, MASession) (MAResult, error)) *FakeMAClient {
	return &FakeMAClient{fn: fn}
}

// RunDeveloperSession records the session and delegates.
func (f *FakeMAClient) RunDeveloperSession(ctx context.Context, req MASession) (MAResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	return f.fn(ctx, req)
}

// Calls returns the sessions seen so far.
func (f *FakeMAClient) Calls() []MASession {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]MASession, len(f.calls))
	copy(out, f.calls)
	return out
}
