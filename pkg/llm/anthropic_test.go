package llm

import (
	"context"
	"os"
	"testing"
)

func TestAnthropicName(t *testing.T) {
	if NewAnthropic().Name() != "anthropic" {
		t.Fatal("Name should be anthropic")
	}
}

// TestAnthropicComplete is an integration test: it only runs when a real API
// key is present. CI without a key skips it. Tests of worker logic use Fake.
func TestAnthropicComplete(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("no ANTHROPIC_API_KEY; skipping live Anthropic call")
	}
	b := NewAnthropic()
	resp, err := b.Complete(context.Background(), Request{
		Model: "claude-haiku-4-5", MaxTokens: 16,
		System:   "You reply with exactly one word.",
		Messages: []Message{{Role: "user", Content: "Say hello."}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Text == "" || resp.Usage.OutputTokens == 0 {
		t.Fatalf("empty response: %+v", resp)
	}
}

// TestAnthropicCompleteToolUse is a live integration test of tool-use
// translation: offering a tool and asserting the model's tool_use block is
// parsed into resp.ToolCalls. Skips without a real API key.
func TestAnthropicCompleteToolUse(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("no ANTHROPIC_API_KEY; skipping live Anthropic call")
	}
	b := NewAnthropic()
	resp, err := b.Complete(context.Background(), Request{
		Model: "claude-haiku-4-5", MaxTokens: 256,
		System: "Use the get_weather tool to answer.",
		Tools: []ToolDef{{
			Name:        "get_weather",
			Description: "Get the current weather for a city",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"city": map[string]any{"type": "string"},
				},
				"required": []string{"city"},
			},
		}},
		Messages: []Message{{Role: "user", Content: "What's the weather in Boston?"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.StopReason != "tool_use" || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "get_weather" {
		t.Fatalf("expected a get_weather tool call, got: %+v", resp)
	}
	if len(resp.ToolCalls[0].Input) == 0 {
		t.Fatalf("expected non-empty tool call input, got: %+v", resp.ToolCalls[0])
	}
}
