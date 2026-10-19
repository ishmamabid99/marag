package mcpgw

import (
	"context"
	"testing"

	"github.com/fin/multi-agent-rag/internal/mcpauth"
)

func TestScopeGate(t *testing.T) {
	gate := ScopeGate()
	// A context (read-only) tool and a full-only tool.
	const ctxTool = "search_code"             // IsContextTool == true
	const fullTool = "submit_feature_request" // IsContextTool == false

	cases := []struct {
		name      string
		scope     string
		tool      string
		wantAllow bool
	}{
		{"full allows full tool", mcpauth.ScopeFull, fullTool, true},
		{"full allows context tool", mcpauth.ScopeFull, ctxTool, true},
		{"context allows context tool", mcpauth.ScopeContext, ctxTool, true},
		{"context denies full tool", mcpauth.ScopeContext, fullTool, false},
		{"unauth denies context tool", "", ctxTool, false},
		{"unauth denies full tool", "", fullTool, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithScope(context.Background(), tc.scope)
			err := gate(ctx, tc.tool)
			if tc.wantAllow && err != nil {
				t.Fatalf("want allow, got error: %v", err)
			}
			if !tc.wantAllow && err == nil {
				t.Fatal("want deny, got nil error")
			}
		})
	}
}

func TestScopeFromContextDefaultEmpty(t *testing.T) {
	if s := ScopeFromContext(context.Background()); s != "" {
		t.Fatalf("no scope should be empty, got %q", s)
	}
}
