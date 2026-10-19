package llm

import (
	"context"
	"testing"
)

// The Thinking flag must round-trip through a Backend so roles can request
// adaptive thinking and tests can assert it was requested.
func TestRequestThinkingRoundTrips(t *testing.T) {
	fake := NewFake(func(_ context.Context, _ Request) (Response, error) {
		return Response{Text: "ok"}, nil
	})
	_, err := fake.Complete(context.Background(), Request{
		Model:     "claude-opus-4-8",
		Thinking:  true,
		MaxTokens: 100,
		Messages:  []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	calls := fake.Calls()
	if len(calls) != 1 || !calls[0].Thinking {
		t.Fatalf("Thinking not recorded: %+v", calls)
	}
}
