package mcpgw

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fin/multi-agent-rag/internal/mcpauth"
)

// echoScope writes the scope the middleware put on the context, so tests can
// assert what BearerAuth resolved.
func echoScope() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("scope=" + ScopeFromContext(r.Context())))
	})
}

func TestBearerAuthResolvesScope(t *testing.T) {
	store := mcpauth.NewFakeStore()
	store.AddScoped("goodfull", "alice", mcpauth.ScopeFull, nil)
	store.AddScoped("goodctx", "bob", mcpauth.ScopeContext, nil)
	h := BearerAuth(store, echoScope())

	cases := []struct{ name, header, want string }{
		{"full token", "Bearer goodfull", "scope=full"},
		{"context token", "Bearer goodctx", "scope=context"},
		{"unknown token", "Bearer nope", "scope="},
		{"no header", "", "scope="},
		{"wrong scheme", "Basic goodfull", "scope="},
		{"case-insensitive scheme", "bearer goodfull", "scope=full"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Body.String() != tc.want {
				t.Fatalf("got %q, want %q", rec.Body.String(), tc.want)
			}
		})
	}
}
