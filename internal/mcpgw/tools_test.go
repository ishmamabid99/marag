package mcpgw

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/fin/multi-agent-rag/internal/gateway"
	"github.com/fin/multi-agent-rag/internal/mcpserver"
)

// fakeProducer + fake readers to back a real gateway.Gateway.
type fakeProd struct{ last string }

func (f *fakeProd) Publish(_ context.Context, topic, key string, _ []byte) error {
	f.last = topic + "|" + key
	return nil
}

type fakeReader struct{}

func (fakeReader) GetRun(_ context.Context, id string) (gateway.RunView, bool, error) {
	return gateway.RunView{RunID: id, Status: "running", CurrentStage: "developing"}, true, nil
}
func (fakeReader) ListRuns(_ context.Context, limit int) ([]gateway.RunView, error) {
	return []gateway.RunView{{RunID: "r1", Status: "running"}}, nil
}
func (fakeReader) LoadArtifacts(_ context.Context, id string) (map[string][]byte, error) {
	return nil, nil
}

type fakeRepos struct{}

func (fakeRepos) ListRepos(_ context.Context) ([]gateway.RepoView, error) {
	return []gateway.RepoView{{ID: "payments", Name: "payments-svc"}}, nil
}

type fakeSearch struct{}

func (fakeSearch) Search(_ context.Context, q string, _ []string, _ int) ([]gateway.SearchHit, error) {
	return []gateway.SearchHit{{RepoID: "payments", Path: "pay.go", Score: 0.9}}, nil
}
func (fakeSearch) GetFile(_ context.Context, repoID, filePath string) (string, bool, error) {
	if filePath == "pay.go" {
		return "package payments\nfunc ProcessPayment() {}\n", true, nil
	}
	return "", false, nil
}

func callTool(t *testing.T, s *mcpserver.Server, name, args string) map[string]any {
	t.Helper()
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + args + `}}`
	resp, ok := s.Handle(context.Background(), []byte(req))
	if !ok {
		t.Fatalf("no response for %s", name)
	}
	var m map[string]any
	_ = json.Unmarshal(resp, &m)
	result := m["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("%s errored: %v", name, result)
	}
	text := result["content"].([]any)[0].(map[string]any)["text"].(string)
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		// some tools may return a bare value; wrap for assertion convenience
		return map[string]any{"_text": text}
	}
	return out
}

func newRegistered() *mcpserver.Server {
	g := gateway.New(&fakeProd{}, fakeReader{}, fakeRepos{}, fakeSearch{})
	s := mcpserver.New("fin-context", "0.1.0")
	RegisterTools(s, g)
	return s
}

func TestSubmitAndStatusTools(t *testing.T) {
	s := newRegistered()
	sub := callTool(t, s, "submit_feature_request", `{"spec":"add currency","repo_ids":["payments"]}`)
	if _, ok := sub["run_id"]; !ok {
		t.Fatalf("submit result missing run_id: %v", sub)
	}
	st := callTool(t, s, "get_run_status", `{"run_id":"r1"}`)
	if st["status"] != "running" {
		t.Fatalf("status = %v", st)
	}
}

func TestListReposAndSearchTools(t *testing.T) {
	s := newRegistered()
	// list_repos returns an array → wrapped as _text; just assert content.
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_repos","arguments":{}}}`
	resp, _ := s.Handle(context.Background(), []byte(req))
	if !strings.Contains(string(resp), "payments-svc") {
		t.Fatalf("list_repos = %s", resp)
	}
	req = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search_code","arguments":{"query":"currency"}}}`
	resp, _ = s.Handle(context.Background(), []byte(req))
	if !strings.Contains(string(resp), "pay.go") {
		t.Fatalf("search_code = %s", resp)
	}
}

func TestApproveRejectTools(t *testing.T) {
	s := newRegistered()
	for _, tool := range []string{"approve_run", "reject_run"} {
		req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":{"run_id":"r1"}}}`
		resp, _ := s.Handle(context.Background(), []byte(req))
		if strings.Contains(string(resp), `"isError":true`) {
			t.Fatalf("%s errored: %s", tool, resp)
		}
	}
}

func TestSubmitMissingSpecErrors(t *testing.T) {
	s := newRegistered()
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"submit_feature_request","arguments":{"spec":""}}}`
	resp, _ := s.Handle(context.Background(), []byte(req))
	if !strings.Contains(string(resp), `"isError":true`) {
		t.Fatalf("expected isError for empty spec: %s", resp)
	}
}

// contextScopeGate mirrors cmd/mcp-gateway's per-tool gate for a context token:
// read-only knowledge-base tools allowed, everything else refused.
func contextScopeGate(_ context.Context, tool string) error {
	if IsContextTool(tool) {
		return nil
	}
	return fmt.Errorf("tool %q requires a full-scope token; this token is context (read-only knowledge base)", tool)
}

func TestGetFileTool(t *testing.T) {
	s := newRegistered()
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_file","arguments":{"repo_id":"payments","file_path":"pay.go"}}}`
	resp, _ := s.Handle(context.Background(), []byte(req))
	if !strings.Contains(string(resp), "ProcessPayment") {
		t.Fatalf("get_file should return the file content: %s", resp)
	}
	// Unknown file → friendly not-indexed message (not a hard error).
	req = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_file","arguments":{"repo_id":"payments","file_path":"nope.go"}}}`
	resp, _ = s.Handle(context.Background(), []byte(req))
	if !strings.Contains(string(resp), "not indexed") {
		t.Fatalf("unknown file should say not indexed: %s", resp)
	}
}

func TestIsContextTool(t *testing.T) {
	for _, name := range []string{"search_code", "list_repos", "get_file"} {
		if !IsContextTool(name) {
			t.Errorf("%q should be a context (read-only) tool", name)
		}
	}
	for _, name := range []string{"submit_feature_request", "approve_run", "reject_run", "get_run_status", "list_runs"} {
		if IsContextTool(name) {
			t.Errorf("%q must NOT be a context tool (it spends budget or exposes run activity)", name)
		}
	}
}

func TestContextScopeAllowsKnowledgeBaseBlocksTriggers(t *testing.T) {
	s := newRegistered()
	s.SetToolCallGate(contextScopeGate)

	// Knowledge-base tools: allowed.
	for _, c := range []struct{ name, args string }{
		{"search_code", `{"query":"currency"}`},
		{"list_repos", `{}`},
	} {
		req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + c.name + `","arguments":` + c.args + `}}`
		resp, _ := s.Handle(context.Background(), []byte(req))
		if strings.Contains(string(resp), `"isError":true`) {
			t.Fatalf("%s must be allowed for context scope: %s", c.name, resp)
		}
	}

	// Budget-spending / mutating tools: blocked with a clear message.
	for _, c := range []struct{ name, args string }{
		{"submit_feature_request", `{"spec":"x","repo_ids":["payments"]}`},
		{"approve_run", `{"run_id":"r1"}`},
		{"reject_run", `{"run_id":"r1"}`},
	} {
		req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + c.name + `","arguments":` + c.args + `}}`
		resp, _ := s.Handle(context.Background(), []byte(req))
		if !strings.Contains(string(resp), `"isError":true`) || !strings.Contains(string(resp), "full-scope") {
			t.Fatalf("%s must be blocked for context scope: %s", c.name, resp)
		}
	}
}
