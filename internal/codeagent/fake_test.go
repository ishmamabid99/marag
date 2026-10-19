package codeagent

import (
	"context"
	"testing"

	"github.com/fin/multi-agent-rag/internal/sandbox"
)

func TestFakeCodeAgentRecordsAndDelegates(t *testing.T) {
	ctx := context.Background()
	sb, err := sandbox.NewLocal(ctx)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })

	fake := NewFake(func(_ context.Context, task Task, sb sandbox.Sandbox) (Outcome, error) {
		if err := sb.WriteFile("main.go", []byte("package main\n")); err != nil {
			return Outcome{}, err
		}
		return Outcome{Summary: "wrote main", FilesChanged: []string{"main.go"}}, nil
	})

	out, err := fake.Implement(ctx, Task{Spec: "x", Attempt: 1}, sb)
	if err != nil {
		t.Fatalf("Implement: %v", err)
	}
	if out.Summary != "wrote main" || len(out.FilesChanged) != 1 {
		t.Fatalf("outcome = %+v", out)
	}
	if got, _ := sb.ReadFile("main.go"); string(got) != "package main\n" {
		t.Fatalf("file content = %q", got)
	}
	if calls := fake.Calls(); len(calls) != 1 || calls[0].Spec != "x" {
		t.Fatalf("calls = %+v", calls)
	}
}
