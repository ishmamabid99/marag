package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testServer (with its "echo" tool) is defined in server_test.go.

func post(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHTTPInitialize(t *testing.T) {
	rec := post(t, testServer().HTTPHandler(),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "protocolVersion") {
		t.Fatalf("no protocolVersion in %s", rec.Body.String())
	}
}

func TestHTTPToolCall(t *testing.T) {
	rec := post(t, testServer().HTTPHandler(),
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"msg":"hi"}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "you said: hi") {
		t.Fatalf("echo not reflected: %s", rec.Body.String())
	}
}

func TestHTTPNotificationGets202(t *testing.T) {
	rec := post(t, testServer().HTTPHandler(),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("notification status = %d, want 202", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("notification should have empty body, got %q", rec.Body.String())
	}
}

func TestHTTPBatch(t *testing.T) {
	rec := post(t, testServer().HTTPHandler(),
		`[{"jsonrpc":"2.0","id":1,"method":"tools/list"},{"jsonrpc":"2.0","method":"notifications/x"}]`)
	if rec.Code != http.StatusOK {
		t.Fatalf("batch status = %d, want 200", rec.Code)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &arr); err != nil {
		t.Fatalf("batch response not a JSON array: %v (%s)", err, rec.Body.String())
	}
	if len(arr) != 1 { // only the request with an id gets a response
		t.Fatalf("batch responses = %d, want 1", len(arr))
	}
}

func TestHTTPMethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	testServer().HTTPHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", rec.Code)
	}
}
