package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fin/multi-agent-rag/pkg/llm"
)

func okBackend() *llm.Fake {
	return llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: "ok"}, nil
	})
}

func TestSessionManagerPassThrough(t *testing.T) {
	m := NewSessionManager(okBackend(), SessionConfig{MaxConcurrent: 2})
	resp, err := m.Complete(context.Background(), llm.Request{Model: "m"})
	if err != nil || resp.Text != "ok" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
}

func TestSessionManagerCircuitOpensAndRecovers(t *testing.T) {
	fail := errors.New("upstream 429")
	failing := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{}, fail
	})
	clock := &fakeClock{t: time.Unix(0, 0)}
	m := newSessionManagerWithClock(failing, SessionConfig{FailureThreshold: 3, Cooldown: time.Minute}, clock.now)

	// 3 consecutive failures trip the breaker.
	for i := 0; i < 3; i++ {
		if _, err := m.Complete(context.Background(), llm.Request{}); !errors.Is(err, fail) {
			t.Fatalf("call %d err = %v, want upstream", i, err)
		}
	}
	// Breaker now open: calls short-circuit without hitting the backend.
	if _, err := m.Complete(context.Background(), llm.Request{}); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}

	// Advance past cooldown → half-open → a success closes the breaker.
	clock.advance(2 * time.Minute)
	m.swapBackend(okBackend()) // trial succeeds
	if _, err := m.Complete(context.Background(), llm.Request{}); err != nil {
		t.Fatalf("half-open trial should succeed: %v", err)
	}
	if _, err := m.Complete(context.Background(), llm.Request{}); err != nil {
		t.Fatalf("breaker should be closed: %v", err)
	}
}

func TestSessionManagerHalfOpenFailureReopens(t *testing.T) {
	fail := errors.New("upstream 500")
	failing := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{}, fail
	})
	clock := &fakeClock{t: time.Unix(0, 0)}
	m := newSessionManagerWithClock(failing, SessionConfig{FailureThreshold: 2, Cooldown: time.Minute}, clock.now)

	// 2 consecutive failures trip the breaker.
	for i := 0; i < 2; i++ {
		if _, err := m.Complete(context.Background(), llm.Request{}); !errors.Is(err, fail) {
			t.Fatalf("call %d err = %v, want upstream", i, err)
		}
	}

	// Breaker open: cooldown not elapsed → short-circuit.
	if _, err := m.Complete(context.Background(), llm.Request{}); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}

	// Advance past cooldown → admitted as the half-open trial → it fails,
	// re-opening the breaker.
	clock.advance(2 * time.Minute)
	if _, err := m.Complete(context.Background(), llm.Request{}); !errors.Is(err, fail) {
		t.Fatalf("half-open trial should hit backend and fail: %v", err)
	}

	// Immediately after (clock unchanged): breaker is re-opened.
	if _, err := m.Complete(context.Background(), llm.Request{}); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen after re-open, got %v", err)
	}

	// Advance past cooldown again, swap to a healthy backend → trial
	// succeeds, breaker closes.
	clock.advance(2 * time.Minute)
	m.swapBackend(okBackend())
	if _, err := m.Complete(context.Background(), llm.Request{}); err != nil {
		t.Fatalf("half-open trial should succeed: %v", err)
	}
	if _, err := m.Complete(context.Background(), llm.Request{}); err != nil {
		t.Fatalf("breaker should be closed: %v", err)
	}
}

func TestSessionManagerRespectsCancellation(t *testing.T) {
	// One slot, held by a blocking call; a second call with a cancelled ctx returns promptly.
	release := make(chan struct{})
	blocking := llm.NewFake(func(ctx context.Context, _ llm.Request) (llm.Response, error) {
		<-release
		return llm.Response{}, nil
	})
	m := NewSessionManager(blocking, SessionConfig{MaxConcurrent: 1})
	go func() { _, _ = m.Complete(context.Background(), llm.Request{}) }()
	time.Sleep(20 * time.Millisecond) // let the goroutine grab the slot

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Complete(ctx, llm.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled call err = %v, want context.Canceled", err)
	}
	close(release)
}

// fakeClock is a manually-advanced clock for breaker tests.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }
