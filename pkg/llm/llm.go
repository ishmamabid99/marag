// Package llm defines the pluggable LLM backend used by agent roles, plus a
// deterministic fake and an Anthropic Messages backend.
package llm

import (
	"context"
	"encoding/json"
)

// Message is one chat turn. For a tool-use loop, an assistant turn may carry
// ToolCalls and the following user turn carries the matching ToolResults.
type Message struct {
	Role        string
	Content     string
	ToolCalls   []ToolCall   // assistant turn: tool_use blocks requested
	ToolResults []ToolResult // user turn: results for prior tool_use blocks
}

// ToolDef declares a tool the model may call. InputSchema is a JSON Schema
// object (at least {"type":"object","properties":{...}}).
type ToolDef struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// ToolCall is a tool invocation the model requested.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult is the outcome of executing a tool, fed back to the model.
type ToolResult struct {
	ToolUseID string
	Content   string
	IsError   bool
}

// Usage reports token consumption for a completion.
type Usage struct {
	InputTokens  int
	OutputTokens int
	// CacheReadTokens are input tokens served from the prompt cache (~90%
	// cheaper). CacheWriteTokens are input tokens written to the cache (~25%
	// surcharge, paid once). Both zero when caching is unused/unsupported.
	CacheReadTokens  int
	CacheWriteTokens int
}

// Request is a single non-interactive completion request.
type Request struct {
	Model     string
	System    string
	Messages  []Message
	MaxTokens int
	// Thinking requests adaptive extended thinking (Claude 4.6+ models only:
	// Opus 4.8, Sonnet 5). Pre-4.6 models (e.g. Haiku 4.5) must leave it false.
	Thinking bool
	// Tools, when non-empty, are offered to the model for tool-use.
	Tools []ToolDef
	// OutputSchema, when non-empty, constrains the reply to a JSON schema at
	// generation time (structured outputs). Optional; nil leaves output free.
	OutputSchema map[string]any
}

// Response is a completion result.
type Response struct {
	Text       string
	StopReason string
	Model      string
	Usage      Usage
	// ToolCalls are the tool invocations the model requested (StopReason "tool_use").
	ToolCalls []ToolCall
}

// Backend is a pluggable LLM completion backend.
type Backend interface {
	Complete(ctx context.Context, req Request) (Response, error)
	Name() string
}
