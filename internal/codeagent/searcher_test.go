package codeagent

import (
	"context"
	"strings"
	"testing"

	"github.com/fin/multi-agent-rag/internal/sandbox"
	"github.com/fin/multi-agent-rag/pkg/llm"
)

func TestToolLoopSearchCodeToolOfferedAndCalled(t *testing.T) {
	ctx := context.Background()
	sb, err := sandbox.NewLocal(ctx)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })

	searcher := &FakeSearcher{Hits: []SearchHit{{RepoID: "payments", Path: "convert.go", Snippet: "func Convert(", Score: 0.91}}}

	step := 0
	backend := llm.NewFake(func(_ context.Context, req llm.Request) (llm.Response, error) {
		step++
		switch step {
		case 1:
			// search_code must be offered when a searcher is attached.
			var names []string
			for _, td := range req.Tools {
				names = append(names, td.Name)
			}
			if !containsStr(names, "search_code") {
				t.Errorf("search_code not offered: %v", names)
			}
			return llm.Response{StopReason: "tool_use", ToolCalls: []llm.ToolCall{
				tc("s1", "search_code", map[string]any{"query": "currency conversion"}),
			}}, nil
		case 2:
			// The search results must be fed back as a tool result.
			last := req.Messages[len(req.Messages)-1]
			if len(last.ToolResults) != 1 || !strings.Contains(last.ToolResults[0].Content, "convert.go") {
				t.Errorf("search result not fed back: %+v", last.ToolResults)
			}
			return llm.Response{StopReason: "end_turn", Text: "used context"}, nil
		default:
			return llm.Response{StopReason: "end_turn", Text: "done"}, nil
		}
	})

	agent := NewLocalToolLoopAgent(backend, "claude-sonnet-5").WithSearcher(searcher)
	out, err := agent.Implement(ctx, Task{Spec: "add currency", RepoIDs: []string{"payments"}, Attempt: 1}, sb)
	if err != nil {
		t.Fatalf("Implement: %v", err)
	}
	if out.Summary != "used context" {
		t.Fatalf("summary = %q", out.Summary)
	}
	if len(searcher.Queries) != 1 || searcher.Queries[0] != "currency conversion" {
		t.Fatalf("searcher queries = %v", searcher.Queries)
	}
}

func TestToolLoopNoSearcherOmitsSearchTool(t *testing.T) {
	ctx := context.Background()
	sb, err := sandbox.NewLocal(ctx)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })

	backend := llm.NewFake(func(_ context.Context, req llm.Request) (llm.Response, error) {
		for _, td := range req.Tools {
			if td.Name == "search_code" {
				t.Errorf("search_code offered without a searcher")
			}
		}
		return llm.Response{StopReason: "end_turn", Text: "done"}, nil
	})
	agent := NewLocalToolLoopAgent(backend, "claude-sonnet-5") // no searcher
	if _, err := agent.Implement(ctx, Task{Spec: "x", Attempt: 1}, sb); err != nil {
		t.Fatalf("Implement: %v", err)
	}
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
