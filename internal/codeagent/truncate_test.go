package codeagent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/fin/multi-agent-rag/pkg/llm"
)

// buildConvo makes [task, (assistant tool_use, user tool_result)×nPairs].
func buildConvo(nPairs int, toolName string) []llm.Message {
	msgs := []llm.Message{{Role: "user", Content: "TASK"}}
	for i := range nPairs {
		id := fmt.Sprintf("t%d", i)
		msgs = append(msgs,
			llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: id, Name: toolName}}},
			llm.Message{Role: "user", ToolResults: []llm.ToolResult{{ToolUseID: id, Content: "res"}}},
		)
	}
	return msgs
}

func TestTruncateTranscriptNoopWhenSmall(t *testing.T) {
	msgs := buildConvo(3, "run_command")
	out := truncateTranscript(msgs, 8)
	if len(out) != len(msgs) {
		t.Fatalf("small transcript should pass through: got %d want %d", len(out), len(msgs))
	}
}

func TestTruncateTranscriptDropsOldPairs(t *testing.T) {
	msgs := buildConvo(12, "write_file")
	origLen := len(msgs)
	origTask := msgs[0].Content

	out := truncateTranscript(msgs, 8)

	if len(out) != 1+8*2 {
		t.Fatalf("len=%d want 17 (task + 8 pairs)", len(out))
	}
	if !strings.Contains(out[0].Content, "omitted 4 earlier") {
		t.Fatalf("task note missing drop count: %q", out[0].Content)
	}
	if !strings.Contains(out[0].Content, "write_file×4") {
		t.Fatalf("task note missing tool counts: %q", out[0].Content)
	}
	// The kept tail must begin with an assistant tool_use (valid alternation
	// after the user task) and every kept pair stays intact.
	if out[1].Role != "assistant" || len(out[1].ToolCalls) == 0 {
		t.Fatalf("tail should start with an assistant tool_use, got %+v", out[1])
	}
	last := out[len(out)-1]
	if last.Role != "user" || len(last.ToolResults) != 1 || last.ToolResults[0].ToolUseID != "t11" {
		t.Fatalf("last message should be the most-recent tool_result (t11): %+v", last)
	}
	// Input must not be mutated.
	if len(msgs) != origLen || msgs[0].Content != origTask {
		t.Fatal("truncateTranscript mutated its input")
	}
}
