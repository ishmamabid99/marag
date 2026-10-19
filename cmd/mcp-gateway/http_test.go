package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fin/multi-agent-rag/internal/gateway"
	"github.com/fin/multi-agent-rag/internal/mcpauth"
	"github.com/fin/multi-agent-rag/internal/mcpgw"
	"github.com/fin/multi-agent-rag/internal/mcpserver"
)

func testMux(store mcpauth.TokenStore) http.Handler {
	g := gateway.New(nil, nil, nil, nil) // deps unused by tools/list / denied calls
	s := mcpserver.New("fin-context", "test")
	mcpgw.RegisterTools(s, g)
	s.SetToolCallGate(mcpgw.ScopeGate())
	return newHTTPMux(s, store)
}

func TestHTTPMuxHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	testMux(mcpauth.NewFakeStore()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}

func TestHTTPMuxToolsListNoAuth(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	testMux(mcpauth.NewFakeStore()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "search_code") {
		t.Fatalf("tools/list should work unauthenticated: %d %s", rec.Code, rec.Body.String())
	}
}

func TestHTTPMuxToolCallDeniedWithoutToken(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_repos","arguments":{}}}`))
	testMux(mcpauth.NewFakeStore()).ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "authentication required") {
		t.Fatalf("unauthenticated tools/call should be refused: %s", rec.Body.String())
	}
}
