package worker

import (
	"context"
	"errors"
	"testing"
)

func TestRetryTransientSucceedsAfterTransientFailure(t *testing.T) {
	calls := 0
	err := retryTransient(context.Background(), 3, 0, func() error {
		calls++
		if calls < 2 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("err=%v calls=%d, want nil,2", err, calls)
	}
}

func TestRetryTransientGivesUpAfterAttempts(t *testing.T) {
	calls := 0
	want := errors.New("always")
	err := retryTransient(context.Background(), 3, 0, func() error {
		calls++
		return want
	})
	if !errors.Is(err, want) || calls != 3 {
		t.Fatalf("err=%v calls=%d, want always,3", err, calls)
	}
}

func TestRetryTransientAbortsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := retryTransient(ctx, 3, 0, func() error {
		calls++
		return errors.New("x")
	})
	if err == nil || calls != 1 {
		t.Fatalf("cancelled ctx should stop after 1 call: err=%v calls=%d", err, calls)
	}
}
