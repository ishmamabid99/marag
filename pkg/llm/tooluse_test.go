package llm

import (
	"context"
	"encoding/json"
	"testing"
)

// A minimal two-step tool loop over the Fake proves the new fields support an
// agentic conversation: request carries Tools; the model returns a ToolCall;
// the caller appends the assistant tool-use turn + a user tool-result turn;
// the second request carries that result and the model replies with text.
func TestFakeSupportsToolLoop(t *testing.T) {
	ctx := context.Background()
	step := 0
	fake := NewFake(func(_ context.Context, req Request) (Response, error) {
		step++
		if step == 1 {
			if len(req.Tools) != 1 || req.Tools[0].Name != "write_file" {
				return Response{}, nil
			}
			return Response{
				StopReason: "tool_use",
				ToolCalls: []ToolCall{{
					ID: "tu_1", Name: "write_file",
					Input: json.RawMessage(`{"path":"a.go","content":"package a"}`),
				}},
			}, nil
		}
		return Response{Text: "done", StopReason: "end_turn"}, nil
	})

	tools := []ToolDef{{
		Name: "write_file", Description: "write a file",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
			"required": []string{"path", "content"},
		},
	}}

	// Step 1.
	msgs := []Message{{Role: "user", Content: "create a.go"}}
	resp, err := fake.Complete(ctx, Request{Model: "claude-opus-4-8", Tools: tools, Messages: msgs, MaxTokens: 1000})
	if err != nil {
		t.Fatalf("step1: %v", err)
	}
	if resp.StopReason != "tool_use" || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "write_file" {
		t.Fatalf("step1 resp = %+v", resp)
	}

	// Caller records the assistant tool-use turn and the user's tool result.
	msgs = append(msgs,
		Message{Role: "assistant", ToolCalls: resp.ToolCalls},
		Message{Role: "user", ToolResults: []ToolResult{{ToolUseID: "tu_1", Content: "ok"}}},
	)

	// Step 2.
	resp, err = fake.Complete(ctx, Request{Model: "claude-opus-4-8", Tools: tools, Messages: msgs, MaxTokens: 1000})
	if err != nil {
		t.Fatalf("step2: %v", err)
	}
	if resp.StopReason != "end_turn" || resp.Text != "done" {
		t.Fatalf("step2 resp = %+v", resp)
	}

	// The Fake recorded both requests, and the 2nd carried the tool result.
	calls := fake.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	last := calls[1].Messages[len(calls[1].Messages)-1]
	if len(last.ToolResults) != 1 || last.ToolResults[0].ToolUseID != "tu_1" {
		t.Fatalf("last message tool results = %+v", last)
	}
}
