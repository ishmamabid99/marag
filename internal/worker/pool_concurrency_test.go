package worker

import (
	"context"
	"testing"
	"time"
)

// TestWithRoleLimitsBoundsConcurrency: a role capped at n admits n holders and
// blocks the (n+1)th until one releases.
func TestWithRoleLimitsBoundsConcurrency(t *testing.T) {
	p := (&Pool{}).WithRoleLimits(map[string]int{"developer": 2, "qa": 0})
	ctx := context.Background()

	// qa mapped to 0 ⇒ unbounded (no semaphore entry).
	if _, ok := p.sems["qa"]; ok {
		t.Fatal("role with limit <=0 must be unbounded (no semaphore)")
	}

	// Acquire the two developer slots.
	rel1, err := p.acquire(ctx, "developer")
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	rel2, err := p.acquire(ctx, "developer")
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}

	// The third acquire must block until a slot frees.
	acquired := make(chan struct{})
	go func() {
		rel3, err := p.acquire(ctx, "developer")
		if err == nil {
			rel3()
		}
		close(acquired)
	}()

	select {
	case <-acquired:
		t.Fatal("third acquire should block while both slots are held")
	case <-time.After(100 * time.Millisecond):
		// expected: still blocked
	}

	rel1() // free one slot → the blocked acquire proceeds
	select {
	case <-acquired:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("third acquire never unblocked after a release")
	}
	rel2()
}

// TestAcquireUnboundedRoleNeverBlocks: a role with no limit returns immediately
// and its release is a safe no-op.
func TestAcquireUnboundedRoleNeverBlocks(t *testing.T) {
	p := (&Pool{}).WithRoleLimits(map[string]int{"developer": 1})
	// "risk-analyzer" is absent from the map ⇒ unbounded.
	for i := range 100 {
		rel, err := p.acquire(context.Background(), "risk-analyzer")
		if err != nil {
			t.Fatalf("unbounded acquire %d: %v", i, err)
		}
		rel()
	}
}

// TestAcquireRespectsCancellation: a full slot + cancelled ctx returns an error
// rather than blocking forever (so the stage is redelivered, not wedged).
func TestAcquireRespectsCancellation(t *testing.T) {
	p := (&Pool{}).WithRoleLimits(map[string]int{"developer": 1})
	rel, err := p.acquire(context.Background(), "developer")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer rel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.acquire(ctx, "developer"); err == nil {
		t.Fatal("acquire on a full role with a cancelled ctx must return an error")
	}
}

// TestWithRoleLimitsEmptyIsUnbounded: no limits ⇒ every role unbounded.
func TestWithRoleLimitsEmptyIsUnbounded(t *testing.T) {
	p := (&Pool{}).WithRoleLimits(nil)
	if p.sems != nil {
		t.Fatal("nil limits must leave the pool unbounded (sems nil)")
	}
	rel, err := p.acquire(context.Background(), "developer")
	if err != nil {
		t.Fatalf("acquire on unbounded pool: %v", err)
	}
	rel()
}
