package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/vertex"
)

// Anthropic is a Backend backed by the Anthropic Messages API (direct or via
// Google Vertex AI).
type Anthropic struct {
	client         anthropic.Client
	modelOverrides map[string]string // canonical model id -> provider-specific id
	noThinking     bool
	noStructured   bool
}

// anthropicBuilder accumulates constructor options: request options for the SDK
// client plus backend-level settings (model overrides, feature toggles).
type anthropicBuilder struct {
	reqOpts        []option.RequestOption
	modelOverrides map[string]string
	noThinking     bool
	noStructured   bool
}

type anthropicOption func(*anthropicBuilder)

// WithAPIKey sets an explicit API key (otherwise ANTHROPIC_API_KEY is used).
func WithAPIKey(key string) anthropicOption {
	return func(b *anthropicBuilder) { b.reqOpts = append(b.reqOpts, option.WithAPIKey(key)) }
}

// WithBaseURL overrides the API base URL (for a proxy/gateway, or tests).
func WithBaseURL(url string) anthropicOption {
	return func(b *anthropicBuilder) { b.reqOpts = append(b.reqOpts, option.WithBaseURL(url)) }
}

// WithVertex routes the client through Google Vertex AI using Application
// Default Credentials for the given region + project (no API key needed).
func WithVertex(ctx context.Context, region, project string) anthropicOption {
	return func(b *anthropicBuilder) {
		b.reqOpts = append(b.reqOpts, vertex.WithGoogleAuth(ctx, region, project))
	}
}

// WithModelOverrides remaps canonical model ids to provider-specific ones (e.g.
// Vertex publisher ids). An unmapped model passes through unchanged.
func WithModelOverrides(m map[string]string) anthropicOption {
	return func(b *anthropicBuilder) { b.modelOverrides = m }
}

// WithFeatureToggles disables adaptive thinking and/or structured outputs, for
// providers (e.g. Vertex) that don't support them.
func WithFeatureToggles(noThinking, noStructured bool) anthropicOption {
	return func(b *anthropicBuilder) { b.noThinking, b.noStructured = noThinking, noStructured }
}

// NewAnthropic constructs the backend. With no options it reads credentials
// from the environment (ANTHROPIC_API_KEY or an ant-login profile).
func NewAnthropic(opts ...anthropicOption) *Anthropic {
	var b anthropicBuilder
	for _, o := range opts {
		o(&b)
	}
	return &Anthropic{
		client:         anthropic.NewClient(b.reqOpts...),
		modelOverrides: b.modelOverrides,
		noThinking:     b.noThinking,
		noStructured:   b.noStructured,
	}
}

func (a *Anthropic) Name() string { return "anthropic" }

// Complete issues a single Messages request. It sends model, max_tokens,
// system, and user messages, plus adaptive thinking when req.Thinking is set
// (Haiku-compatible when Thinking is left false).
func (a *Anthropic) Complete(ctx context.Context, req Request) (Response, error) {
	msgs := make([]anthropic.MessageParam, 0, len(req.Messages))
	for _, m := range req.Messages {
		switch {
		case len(m.ToolResults) > 0:
			// User turn carrying tool_result blocks for prior tool_use calls.
			blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.ToolResults)+1)
			if m.Content != "" {
				blocks = append(blocks, anthropic.NewTextBlock(m.Content))
			}
			for _, tr := range m.ToolResults {
				blocks = append(blocks, anthropic.NewToolResultBlock(tr.ToolUseID, tr.Content, tr.IsError))
			}
			msgs = append(msgs, anthropic.NewUserMessage(blocks...))
		case len(m.ToolCalls) > 0:
			// Assistant turn carrying tool_use blocks the model requested. Preserve
			// any narration text that accompanied the tool calls (the model often
			// explains a call in the same turn) so multi-turn context survives.
			blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.ToolCalls)+1)
			if m.Content != "" {
				blocks = append(blocks, anthropic.NewTextBlock(m.Content))
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, anthropic.NewToolUseBlock(tc.ID, tc.Input, tc.Name))
			}
			msgs = append(msgs, anthropic.NewAssistantMessage(blocks...))
		case m.Role == "assistant":
			msgs = append(msgs, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content)))
		default:
			// Only user messages are used by the current non-interactive roles.
			msgs = append(msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		}
	}
	// For multi-turn tool loops, add an incremental cache breakpoint at the last
	// message: each turn re-sends the growing history, so caching the prefix up
	// to this turn means the next turn bills only its new tokens at full price.
	// Skipped for single-shot calls (one message) — no reusable prefix.
	if len(msgs) > 1 {
		if blocks := msgs[len(msgs)-1].Content; len(blocks) > 0 {
			if cc := blocks[len(blocks)-1].GetCacheControl(); cc != nil {
				*cc = anthropic.NewCacheControlEphemeralParam()
			}
		}
	}

	model := req.Model
	if m, ok := a.modelOverrides[model]; ok && m != "" {
		model = m
	}
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: int64(req.MaxTokens),
		Messages:  msgs,
	}
	if req.System != "" {
		// Cache the system prompt (role prompt + skill rulesets): it's identical
		// across every call for a role, so after the first write it's served from
		// cache (~90% cheaper) on the developer's tool-loop turns, contract
		// retries, and repeated runs within the cache TTL. A prefix below the
		// model's minimum cacheable size is silently not cached (no error).
		params.System = []anthropic.TextBlockParam{{
			Text:         req.System,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}}
	}
	if req.Thinking && !a.noThinking {
		params.Thinking = anthropic.ThinkingConfigParamUnion{
			OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{},
		}
	}
	if len(req.Tools) > 0 {
		tools := make([]anthropic.ToolUnionParam, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema := anthropic.ToolInputSchemaParam{}
			if props, ok := t.InputSchema["properties"]; ok {
				schema.Properties = props
			}
			schema.Required = requiredFields(t.InputSchema["required"])
			tools = append(tools, anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
				Name:        t.Name,
				Description: anthropic.String(t.Description),
				InputSchema: schema,
			}})
		}
		// Cache the tool definitions (stable across the developer's tool-loop):
		// a breakpoint on the last tool caches the whole tool block, so their
		// schemas aren't re-billed every turn.
		if last := tools[len(tools)-1].OfTool; last != nil {
			last.CacheControl = anthropic.NewCacheControlEphemeralParam()
		}
		params.Tools = tools
	}
	if len(req.OutputSchema) > 0 && !a.noStructured {
		params.OutputConfig = anthropic.OutputConfigParam{
			Format: anthropic.JSONOutputFormatParam{Schema: req.OutputSchema},
		}
	}

	msg, err := a.client.Messages.New(ctx, params)
	if err != nil {
		return Response{}, fmt.Errorf("anthropic complete: %w", err)
	}

	var text strings.Builder
	var toolCalls []ToolCall
	for _, block := range msg.Content {
		switch b := block.AsAny().(type) {
		case anthropic.TextBlock:
			text.WriteString(b.Text)
		case anthropic.ToolUseBlock:
			toolCalls = append(toolCalls, ToolCall{ID: b.ID, Name: b.Name, Input: b.Input})
		}
	}
	return Response{
		Text:       text.String(),
		StopReason: string(msg.StopReason),
		Model:      string(msg.Model),
		Usage: Usage{
			InputTokens:      int(msg.Usage.InputTokens),
			OutputTokens:     int(msg.Usage.OutputTokens),
			CacheReadTokens:  int(msg.Usage.CacheReadInputTokens),
			CacheWriteTokens: int(msg.Usage.CacheCreationInputTokens),
		},
		ToolCalls:  toolCalls,
	}, nil
}

// requiredFields normalizes a tool schema's "required" entry, which may be
// []string (Go-literal schema) or []any (a schema decoded from JSON), so the
// constraint is never silently dropped.
func requiredFields(v any) []string {
	switch r := v.(type) {
	case []string:
		return r
	case []any:
		out := make([]string, 0, len(r))
		for _, item := range r {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
