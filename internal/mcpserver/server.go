// Package mcpserver is a minimal, dependency-free MCP server: JSON-RPC 2.0 over
// newline-delimited stdio, supporting initialize / tools/list / tools/call.
package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const protocolVersion = "2024-11-05"

// ToolHandler executes a tool call; the returned string is the text content.
type ToolHandler func(ctx context.Context, args json.RawMessage) (string, error)

// Tool is a registered MCP tool.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     ToolHandler
}

// Server dispatches MCP JSON-RPC messages to registered tools.
type Server struct {
	name, version string
	tools         map[string]Tool
	order         []string
	toolCallGate  func(ctx context.Context, toolName string) error
}

// SetToolCallGate installs a predicate checked before every tools/call, given
// the requested tool name; a non-nil error refuses the call (initialize /
// tools/list are unaffected). Enables per-tool authorization (e.g. scopes).
func (s *Server) SetToolCallGate(fn func(ctx context.Context, toolName string) error) {
	s.toolCallGate = fn
}

// New creates a server advertised with the given name/version.
func New(name, version string) *Server {
	return &Server{name: name, version: version, tools: map[string]Tool{}}
}

// AddTool registers a tool (last registration of a name wins; order preserved).
func (s *Server) AddTool(t Tool) {
	if _, exists := s.tools[t.Name]; !exists {
		s.order = append(s.order, t.Name)
	}
	s.tools[t.Name] = t
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // absent for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// Handle dispatches one JSON-RPC message. hasResponse is false for
// notifications (no id) and unparseable input that isn't a request.
func (s *Server) Handle(ctx context.Context, raw []byte) ([]byte, bool) {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, false
	}
	// A message with no id is a notification: never respond.
	if len(req.ID) == 0 {
		return nil, false
	}
	switch req.Method {
	case "initialize":
		return s.reply(req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.name, "version": s.version},
		}), true
	case "tools/list":
		return s.reply(req.ID, map[string]any{"tools": s.toolSchemas()}), true
	case "tools/call":
		return s.handleToolCall(ctx, req), true
	default:
		return s.replyError(req.ID, -32601, "method not found: "+req.Method), true
	}
}

func (s *Server) handleToolCall(ctx context.Context, req rpcRequest) []byte {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return s.toolResult(req.ID, "invalid params: "+err.Error(), true)
	}
	// Gate AFTER parsing the name so authorization can be per-tool (scopes).
	if s.toolCallGate != nil {
		if err := s.toolCallGate(ctx, p.Name); err != nil {
			return s.toolResult(req.ID, err.Error(), true)
		}
	}
	tool, ok := s.tools[p.Name]
	if !ok {
		return s.toolResult(req.ID, "unknown tool: "+p.Name, true)
	}
	text, err := tool.Handler(ctx, p.Arguments)
	if err != nil {
		return s.toolResult(req.ID, err.Error(), true)
	}
	return s.toolResult(req.ID, text, false)
}

func (s *Server) toolSchemas() []map[string]any {
	out := make([]map[string]any, 0, len(s.order))
	for _, name := range s.order {
		t := s.tools[name]
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": schema})
	}
	return out
}

func (s *Server) toolResult(id json.RawMessage, text string, isErr bool) []byte {
	return s.reply(id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isErr,
	})
}

func (s *Server) reply(id json.RawMessage, result any) []byte {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
	return b
}

func (s *Server) replyError(id json.RawMessage, code int, msg string) []byte {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "error": map[string]any{"code": code, "message": msg}})
	return b
}

// maxHTTPBody bounds a single MCP HTTP request body (matches the stdio scanner).
const maxHTTPBody = 8 * 1024 * 1024

// HTTPHandler serves the MCP Streamable-HTTP transport: a JSON-RPC message (or
// batch) is POSTed and answered with a JSON response. It is STATELESS (no
// session id) and returns JSON, not SSE — sufficient for request/response
// tools. Authentication is the caller's responsibility: wrap this with
// middleware that authenticates the request and injects the caller's scope into
// the request context (see mcpgw.BearerAuth + mcpgw.ScopeGate); this handler
// only dispatches. Notifications (no id) are answered 202 with an empty body.
func (s *Server) HTTPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()
		body, err := io.ReadAll(io.LimitReader(r.Body, maxHTTPBody))
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		trimmed := bytes.TrimSpace(body)

		// JSON-RPC batch: respond with an array of the responses (skip
		// notifications). If every element is a notification, return 202.
		if len(trimmed) > 0 && trimmed[0] == '[' {
			var msgs []json.RawMessage
			if err := json.Unmarshal(trimmed, &msgs); err != nil {
				http.Error(w, "invalid batch", http.StatusBadRequest)
				return
			}
			responses := make([]json.RawMessage, 0, len(msgs))
			for _, m := range msgs {
				if resp, ok := s.Handle(ctx, m); ok {
					responses = append(responses, resp)
				}
			}
			if len(responses) == 0 {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			out, _ := json.Marshal(responses)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(out)
			return
		}

		resp, ok := s.Handle(ctx, trimmed)
		if !ok { // notification or unparseable-as-request
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(resp)
	})
}

// Serve reads newline-delimited JSON-RPC messages until EOF or ctx cancel,
// writing a newline-terminated response for each request (notifications get none).
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // allow large tool args
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		resp, ok := s.Handle(ctx, line)
		if !ok {
			continue
		}
		if _, err := w.Write(append(resp, '\n')); err != nil {
			return fmt.Errorf("mcpserver write: %w", err)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("mcpserver read: %w", err)
	}
	return nil
}
