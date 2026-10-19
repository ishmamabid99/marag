package llm

import (
	"context"
	"sync"
)

// Fake is a deterministic Backend for tests: it records requests and returns
// whatever its function produces.
type Fake struct {
	fn    func(ctx context.Context, req Request) (Response, error)
	mu    sync.Mutex
	calls []Request
}

// NewFake wraps a response function.
func NewFake(fn func(ctx context.Context, req Request) (Response, error)) *Fake {
	return &Fake{fn: fn}
}

func (f *Fake) Name() string { return "fake" }

// Complete records the request and delegates to the wrapped function.
func (f *Fake) Complete(ctx context.Context, req Request) (Response, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	return f.fn(ctx, req)
}

// Calls returns a copy of the requests seen so far.
func (f *Fake) Calls() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Request, len(f.calls))
	copy(out, f.calls)
	return out
}
