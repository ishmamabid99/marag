package mcpgw

import (
	"context"
	"fmt"

	"github.com/fin/multi-agent-rag/internal/mcpauth"
)

type scopeKey struct{}

// WithScope returns a context carrying the authenticated caller's scope. The
// transport (stdio or HTTP) sets this once it has authenticated the caller;
// ScopeGate reads it per tools/call.
func WithScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope)
}

// ScopeFromContext returns the scope set by WithScope, or "" if none (which
// ScopeGate treats as unauthenticated → all tool calls denied).
func ScopeFromContext(ctx context.Context) string {
	s, _ := ctx.Value(scopeKey{}).(string)
	return s
}

// ScopeGate is the per-tool authorization predicate for
// mcpserver.Server.SetToolCallGate. It reads the scope from ctx:
//   - full    → every tool
//   - context → context (read-only knowledge-base) tools only
//   - ""/other→ unauthenticated: every tools/call denied (initialize/tools/list
//     still respond, unaffected by the gate)
func ScopeGate() func(context.Context, string) error {
	return func(ctx context.Context, tool string) error {
		switch ScopeFromContext(ctx) {
		case mcpauth.ScopeFull:
			return nil
		case mcpauth.ScopeContext:
			if IsContextTool(tool) {
				return nil
			}
			return fmt.Errorf("tool %q requires a full-scope token; this token is context (read-only knowledge base)", tool)
		default:
			return fmt.Errorf("authentication required: present a valid MCP token")
		}
	}
}
