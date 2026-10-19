// Package mcpgw registers the gateway's capabilities as MCP tools.
package mcpgw

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fin/multi-agent-rag/internal/gateway"
	"github.com/fin/multi-agent-rag/internal/mcpserver"
)

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}
func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func jsonStr(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal result: %w", err)
	}
	return string(b), nil
}

// contextTools are the read-only knowledge-base tools a context-scoped token
// may call — they hit only local retrieval/registry (no Anthropic spend). Every
// other tool (submit/approve/reject and run introspection) requires full scope.
var contextTools = map[string]bool{
	"search_code": true,
	"list_repos":  true,
	"get_file":    true,
}

// IsContextTool reports whether name is a read-only knowledge-base tool callable
// by a context-scoped principal.
func IsContextTool(name string) bool { return contextTools[name] }

// RegisterTools registers all gateway tools on the MCP server.
func RegisterTools(s *mcpserver.Server, g *gateway.Gateway) {
	s.AddTool(mcpserver.Tool{
		Name:        "submit_feature_request",
		Description: "Start an autonomous run that implements a feature across the given repos. Returns a run_id; poll get_run_status.",
		InputSchema: obj(map[string]any{
			"spec":     strProp("the feature request / PRD text"),
			"repo_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "candidate repo ids"},
		}, "spec"),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Spec    string   `json:"spec"`
				RepoIDs []string `json:"repo_ids"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("bad arguments: %w", err)
			}
			id, err := g.SubmitRun(ctx, in.Spec, in.RepoIDs)
			if err != nil {
				return "", err
			}
			return jsonStr(map[string]string{"run_id": id, "status": "submitted"})
		},
	})

	s.AddTool(mcpserver.Tool{
		Name:        "get_run_status",
		Description: "Get the current status, stage, and artifacts of a run.",
		InputSchema: obj(map[string]any{"run_id": strProp("the run id")}, "run_id"),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				RunID string `json:"run_id"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("bad arguments: %w", err)
			}
			v, err := g.RunStatus(ctx, in.RunID)
			if err != nil {
				return "", err
			}
			return jsonStr(v)
		},
	})

	s.AddTool(mcpserver.Tool{
		Name:        "list_runs",
		Description: "List recent runs.",
		InputSchema: obj(map[string]any{"limit": map[string]any{"type": "integer", "description": "max runs (default 20)"}}),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Limit int `json:"limit"`
			}
			_ = json.Unmarshal(args, &in)
			runs, err := g.ListRuns(ctx, in.Limit)
			if err != nil {
				return "", err
			}
			return jsonStr(runs)
		},
	})

	resume := func(approved bool) mcpserver.ToolHandler {
		return func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				RunID string `json:"run_id"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("bad arguments: %w", err)
			}
			if err := g.ResumeGate(ctx, in.RunID, approved); err != nil {
				return "", err
			}
			return jsonStr(map[string]any{"run_id": in.RunID, "approved": approved})
		}
	}
	s.AddTool(mcpserver.Tool{Name: "approve_run", Description: "Approve a run parked at a human gate (continue the pipeline).",
		InputSchema: obj(map[string]any{"run_id": strProp("the run id")}, "run_id"), Handler: resume(true)})
	s.AddTool(mcpserver.Tool{Name: "reject_run", Description: "Reject a run parked at a human gate (fail + compensate).",
		InputSchema: obj(map[string]any{"run_id": strProp("the run id")}, "run_id"), Handler: resume(false)})

	s.AddTool(mcpserver.Tool{
		Name:        "list_repos",
		Description: "List all registered codebases available for context and implementation.",
		InputSchema: obj(map[string]any{}),
		Handler: func(ctx context.Context, _ json.RawMessage) (string, error) {
			repos, err := g.ListRepos(ctx)
			if err != nil {
				return "", err
			}
			return jsonStr(repos)
		},
	})

	s.AddTool(mcpserver.Tool{
		Name:        "search_code",
		Description: "Semantic search across all (or the given) registered codebases. Use for cross-repo understanding.",
		InputSchema: obj(map[string]any{
			"query":    strProp("natural-language or code query"),
			"repo_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "restrict to these repos (optional)"},
			"k":        map[string]any{"type": "integer", "description": "max hits (default 10)"},
		}, "query"),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				Query   string   `json:"query"`
				RepoIDs []string `json:"repo_ids"`
				K       int      `json:"k"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("bad arguments: %w", err)
			}
			hits, err := g.SearchContext(ctx, in.Query, in.RepoIDs, in.K)
			if err != nil {
				return "", err
			}
			return jsonStr(hits)
		},
	})

	s.AddTool(mcpserver.Tool{
		Name:        "get_file",
		Description: "Read a whole indexed file's contents from a registered repo (reassembled from its chunks). Use after search_code to read a file end-to-end.",
		InputSchema: obj(map[string]any{
			"repo_id":   strProp("the repo id (from list_repos)"),
			"file_path": strProp("repo-relative file path (from a search_code hit)"),
		}, "repo_id", "file_path"),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				RepoID   string `json:"repo_id"`
				FilePath string `json:"file_path"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return "", fmt.Errorf("bad arguments: %w", err)
			}
			content, found, err := g.GetFile(ctx, in.RepoID, in.FilePath)
			if err != nil {
				return "", err
			}
			if !found {
				return fmt.Sprintf("file %q is not indexed for repo %q (check list_repos / search_code for the exact path)", in.FilePath, in.RepoID), nil
			}
			return content, nil
		},
	})
}
