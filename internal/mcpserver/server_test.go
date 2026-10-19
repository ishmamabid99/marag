package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testServer() *Server {
	s := New("test-mcp", "0.1.0")
	s.AddTool(Tool{
		Name:        "echo",
		Description: "echo back the message",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"msg": map[string]any{"type": "string"}}, "required": []string{"msg"}},
		Handler: func(_ context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Msg string `json:"msg"`
			}
			_ = json.Unmarshal(args, &in)
			return "you said: " + in.Msg, nil
		},
	})
	return s
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal resp: %v (%s)", err, b)
	}
	return m
}

func TestInitialize(t *testing.T) {
	s := testServer()
	resp, ok := s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`))
	if !ok {
		t.Fatal("expected a response")
	}
	m := decode(t, resp)
	result := m["result"].(map[string]any)
	if result["protocolVersion"] != "2024-11-05" {
		t.Fatalf("protocolVersion = %v", result["protocolVersion"])
	}
	if _, hasTools := result["capabilities"].(map[string]any)["tools"]; !hasTools {
		t.Fatalf("missing tools capability: %v", result["capabilities"])
	}
}

func TestInitializedNotificationHasNoResponse(t *testing.T) {
	s := testServer()
	if _, ok := s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); ok {
		t.Fatal("notification must not produce a response")
	}
}

func TestToolsList(t *testing.T) {
	s := testServer()
	resp, _ := s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))
	m := decode(t, resp)
	tools := m["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "echo" {
		t.Fatalf("tools = %v", tools)
	}
}

func TestToolsCall(t *testing.T) {
	s := testServer()
	resp, _ := s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"msg":"hi"}}}`))
	m := decode(t, resp)
	result := m["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("unexpected error: %v", result)
	}
	content := result["content"].([]any)[0].(map[string]any)
	if content["type"] != "text" || !strings.Contains(content["text"].(string), "you said: hi") {
		t.Fatalf("content = %v", content)
	}
}

func TestToolsCallUnknownToolIsError(t *testing.T) {
	s := testServer()
	resp, _ := s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope","arguments":{}}}`))
	m := decode(t, resp)
	// Unknown tool → tools/call result with isError true (not a JSON-RPC error).
	if m["result"].(map[string]any)["isError"] != true {
		t.Fatalf("expected isError=true, got %v", m["result"])
	}
}

func TestUnknownMethodIsJSONRPCError(t *testing.T) {
	s := testServer()
	resp, _ := s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":5,"method":"bogus"}`))
	m := decode(t, resp)
	if m["error"].(map[string]any)["code"] != float64(-32601) {
		t.Fatalf("expected -32601, got %v", m["error"])
	}
}

func TestServeLoop(t *testing.T) {
	s := testServer()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	var out strings.Builder
	if err := s.Serve(context.Background(), in, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if !strings.Contains(out.String(), `"echo"`) {
		t.Fatalf("serve output = %s", out.String())
	}
}

func TestToolCallGateDenies(t *testing.T) {
	s := New("t", "1")
	s.AddTool(Tool{Name: "ping", Handler: func(context.Context, json.RawMessage) (string, error) { return "pong", nil }})
	s.SetToolCallGate(func(context.Context, string) error { return errors.New("authentication required") })

	// tools/list still works (not gated).
	resp, ok := s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if !ok || !strings.Contains(string(resp), "ping") {
		t.Fatalf("tools/list should work under a gate: ok=%v resp=%s", ok, resp)
	}
	// tools/call is denied.
	resp, ok = s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ping"}}`))
	if !ok || !strings.Contains(string(resp), "authentication required") {
		t.Fatalf("tools/call should be denied by gate: ok=%v resp=%s", ok, resp)
	}
	if strings.Contains(string(resp), "pong") {
		t.Fatalf("gated call must not execute the handler")
	}
}

func TestGateReceivesToolName(t *testing.T) {
	s := New("t", "1")
	s.AddTool(Tool{Name: "ping", Handler: func(context.Context, json.RawMessage) (string, error) { return "pong", nil }})
	var got string
	s.SetToolCallGate(func(_ context.Context, name string) error { got = name; return nil })
	s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ping"}}`))
	if got != "ping" {
		t.Fatalf("gate received tool name %q, want ping", got)
	}
}
