package codeagent

import (
	"context"
	"sync"

	"github.com/fin/multi-agent-rag/internal/sandbox"
)

// FakeCodeAgent is a deterministic CodeAgent for tests: it records tasks and
// delegates to a supplied function.
type FakeCodeAgent struct {
	fn    func(ctx context.Context, task Task, sb sandbox.Sandbox) (Outcome, error)
	mu    sync.Mutex
	calls []Task
}

// NewFake wraps an implement function.
func NewFake(fn func(ctx context.Context, task Task, sb sandbox.Sandbox) (Outcome, error)) *FakeCodeAgent {
	return &FakeCodeAgent{fn: fn}
}

// Implement records the task and delegates.
func (f *FakeCodeAgent) Implement(ctx context.Context, task Task, sb sandbox.Sandbox) (Outcome, error) {
	f.mu.Lock()
	f.calls = append(f.calls, task)
	f.mu.Unlock()
	return f.fn(ctx, task, sb)
}

// Calls returns a copy of the tasks seen so far.
func (f *FakeCodeAgent) Calls() []Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Task, len(f.calls))
	copy(out, f.calls)
	return out
}
