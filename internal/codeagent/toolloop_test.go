package codeagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fin/multi-agent-rag/internal/sandbox"
	"github.com/fin/multi-agent-rag/pkg/llm"
)

func tc(id, name string, input map[string]any) llm.ToolCall {
	b, _ := json.Marshal(input)
	return llm.ToolCall{ID: id, Name: name, Input: b}
}

func TestToolLoopWritesFilesRunsCommandThenStops(t *testing.T) {
	ctx := context.Background()
	sb, err := sandbox.NewLocal(ctx)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })

	step := 0
	backend := llm.NewFake(func(_ context.Context, req llm.Request) (llm.Response, error) {
		step++
		switch step {
		case 1:
			// Model must have been offered the three tools.
			names := map[string]bool{}
			for _, tdef := range req.Tools {
				names[tdef.Name] = true
			}
			if !names["write_file"] || !names["read_file"] || !names["run_command"] {
				t.Errorf("tools offered = %v", names)
			}
			return llm.Response{StopReason: "tool_use", ToolCalls: []llm.ToolCall{
				tc("1", "write_file", map[string]any{"path": "go.mod", "content": "module tl\n\ngo 1.21\n"}),
				tc("2", "write_file", map[string]any{"path": "calc.go", "content": "package tl\n\nfunc Add(a, b int) int { return a + b }\n"}),
			}}, nil
		case 2:
			// The prior tool results must be present in the conversation.
			last := req.Messages[len(req.Messages)-1]
			if len(last.ToolResults) != 2 {
				t.Errorf("expected 2 tool results, got %+v", last.ToolResults)
			}
			return llm.Response{StopReason: "tool_use", ToolCalls: []llm.ToolCall{
				tc("3", "run_command", map[string]any{"command": "go build ./..."}),
			}}, nil
		default:
			// run_command result should report success (exit 0).
			last := req.Messages[len(req.Messages)-1]
			if len(last.ToolResults) != 1 || !strings.Contains(last.ToolResults[0].Content, "exit=0") {
				t.Errorf("run result = %+v", last.ToolResults)
			}
			return llm.Response{StopReason: "end_turn", Text: "Implemented Add"}, nil
		}
	})

	agent := NewLocalToolLoopAgent(backend, "claude-sonnet-5")
	out, err := agent.Implement(ctx, Task{Spec: "add Add(a,b)", Attempt: 1}, sb)
	if err != nil {
		t.Fatalf("Implement: %v", err)
	}
	if out.Summary != "Implemented Add" {
		t.Fatalf("summary = %q", out.Summary)
	}
	// Files were actually written to the sandbox.
	if got, err := sb.ReadFile("calc.go"); err != nil || !strings.Contains(string(got), "func Add") {
		t.Fatalf("calc.go = %q err=%v", got, err)
	}
	// FilesChanged reflects the write_file calls.
	seen := map[string]bool{}
	for _, f := range out.FilesChanged {
		seen[f] = true
	}
	if !seen["go.mod"] || !seen["calc.go"] {
		t.Fatalf("FilesChanged = %v", out.FilesChanged)
	}
}

func TestToolLoopBudgetExceeded(t *testing.T) {
	ctx := context.Background()
	sb, err := sandbox.NewLocal(ctx)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })

	// Model never stops calling tools → budget exceeded.
	backend := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{StopReason: "tool_use", ToolCalls: []llm.ToolCall{
			tc("x", "read_file", map[string]any{"path": "go.mod"}),
		}}, nil
	})
	agent := NewLocalToolLoopAgent(backend, "claude-sonnet-5")
	agent.maxTurns = 3
	_, err = agent.Implement(ctx, Task{Spec: "x", Attempt: 1}, sb)
	if err == nil || !errorsIs(err) {
		t.Fatalf("err = %v, want ErrToolLoopExceeded", err)
	}
}

func errorsIs(err error) bool { return err != nil && strings.Contains(err.Error(), "turn budget") }

func TestToolLoopTruncatesLargeCommandOutput(t *testing.T) {
	ctx := context.Background()
	sb, err := sandbox.NewLocal(ctx)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })

	var captured string
	step := 0
	backend := llm.NewFake(func(_ context.Context, req llm.Request) (llm.Response, error) {
		step++
		if step == 1 {
			// print far more than the cap, then stop.
			return llm.Response{StopReason: "tool_use", ToolCalls: []llm.ToolCall{
				tc("1", "run_command", map[string]any{"command": "for i in $(seq 1 5000); do echo LINE_$i; done"}),
			}}, nil
		}
		// capture the tool result that was fed back.
		last := req.Messages[len(req.Messages)-1]
		if len(last.ToolResults) == 1 {
			captured = last.ToolResults[0].Content
		}
		return llm.Response{StopReason: "end_turn", Text: "done"}, nil
	})

	agent := NewLocalToolLoopAgent(backend, "claude-sonnet-5")
	if _, err := agent.Implement(ctx, Task{Spec: "x", Attempt: 1}, sb); err != nil {
		t.Fatalf("Implement: %v", err)
	}
	if len(captured) > maxToolResultBytes+200 { // cap + marker slack
		t.Fatalf("tool result not truncated: %d bytes", len(captured))
	}
	if !strings.Contains(captured, "truncated") {
		t.Fatalf("missing truncation marker: %q", captured[:min(200, len(captured))])
	}
	if !strings.Contains(captured, "exit=") {
		t.Fatalf("exit code line lost")
	}
}
