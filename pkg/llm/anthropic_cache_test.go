package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockMessagesServer returns a server that records the last request body and
// replies with a minimal valid Messages response (incl. cache usage fields).
func mockMessagesServer(t *testing.T, gotBody *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m","type":"message","role":"assistant","model":"claude-haiku-4-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2,"cache_creation_input_tokens":8,"cache_read_input_tokens":5}}`))
	}))
}

func TestAnthropicMultiTurnSetsCacheBreakpoints(t *testing.T) {
	var body string
	srv := mockMessagesServer(t, &body)
	defer srv.Close()
	b := NewAnthropic(WithAPIKey("test"), WithBaseURL(srv.URL))
	resp, err := b.Complete(context.Background(), Request{
		Model: "claude-haiku-4-5", MaxTokens: 16, System: "sys prompt",
		Tools: []ToolDef{{Name: "t", Description: "d", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}},
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
			{Role: "user", Content: "again"},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// system + last tool + last message = 3 cache breakpoints.
	if n := strings.Count(body, "cache_control"); n < 3 {
		t.Fatalf("expected >=3 cache_control breakpoints, got %d in:\n%s", n, body)
	}
	// Cache usage is surfaced.
	if resp.Usage.CacheWriteTokens != 8 || resp.Usage.CacheReadTokens != 5 {
		t.Fatalf("cache usage not parsed: %+v", resp.Usage)
	}
}

func TestAnthropicSingleShotNoMessageBreakpoint(t *testing.T) {
	var body string
	srv := mockMessagesServer(t, &body)
	defer srv.Close()
	b := NewAnthropic(WithAPIKey("test"), WithBaseURL(srv.URL))
	_, err := b.Complete(context.Background(), Request{
		Model: "claude-haiku-4-5", MaxTokens: 16, System: "sys prompt",
		Messages: []Message{{Role: "user", Content: "one shot"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// Only the system prompt is cached (no tools, single message → no message
	// breakpoint): exactly one cache_control.
	if n := strings.Count(body, "cache_control"); n != 1 {
		t.Fatalf("single-shot should have exactly 1 cache_control (system), got %d in:\n%s", n, body)
	}
}
