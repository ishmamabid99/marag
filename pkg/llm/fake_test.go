package llm

import (
	"context"
	"errors"
	"testing"
)

func TestFakeRecordsAndReturns(t *testing.T) {
	want := Response{Text: "hello", StopReason: "end_turn", Model: "m", Usage: Usage{InputTokens: 3, OutputTokens: 1}}
	f := NewFake(func(_ context.Context, _ Request) (Response, error) { return want, nil })
	if f.Name() != "fake" {
		t.Fatalf("Name = %q", f.Name())
	}
	got, err := f.Complete(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got.Text != "hello" || got.Usage.InputTokens != 3 {
		t.Fatalf("resp = %+v", got)
	}
	calls := f.Calls()
	if len(calls) != 1 || calls[0].Messages[0].Content != "hi" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestFakeError(t *testing.T) {
	sentinel := errors.New("boom")
	f := NewFake(func(_ context.Context, _ Request) (Response, error) { return Response{}, sentinel })
	if _, err := f.Complete(context.Background(), Request{}); !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want boom", err)
	}
	if len(f.Calls()) != 1 {
		t.Fatalf("call not recorded on error")
	}
}
