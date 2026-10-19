package codeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/fin/multi-agent-rag/internal/sandbox"
	"github.com/fin/multi-agent-rag/internal/skills"
	"github.com/fin/multi-agent-rag/pkg/llm"
)

// ErrToolLoopExceeded means the model kept calling tools past the turn budget.
var ErrToolLoopExceeded = errors.New("codeagent: tool loop exceeded turn budget")

const maxToolTurns = 40

// maxTranscriptPairs bounds how many recent tool-interaction pairs (an
// assistant tool_use turn + its user tool_result turn) are sent to the model.
// Older pairs are dropped and replaced with a short deterministic note, so the
// developer loop's context stays bounded instead of growing every turn (the
// dominant token cost). The sandbox is the source of truth — the agent can
// re-read files if it needs the current contents of a dropped interaction.
const maxTranscriptPairs = 8

// truncateTranscript returns a bounded VIEW of the conversation for one request:
// the task message (with a note about omitted history) followed by the most
// recent keepPairs turn-pairs. It drops whole (assistant→user) pairs so every
// tool_use stays paired with its tool_result, and never mutates msgs. Assumes
// msgs is [task, (assistant, user)...] with complete pairs (true at each
// Complete call in the loop).
func truncateTranscript(msgs []llm.Message, keepPairs int) []llm.Message {
	if len(msgs) == 0 {
		return msgs
	}
	bodyPairs := (len(msgs) - 1) / 2
	if bodyPairs <= keepPairs {
		return msgs
	}
	dropped := bodyPairs - keepPairs
	tailStart := 1 + dropped*2 // index of the first kept pair's assistant turn

	counts := map[string]int{}
	for _, m := range msgs[1:tailStart] {
		for _, tc := range m.ToolCalls {
			counts[tc.Name]++
		}
	}
	task := msgs[0] // copy (value); Content reassigned below, original untouched
	task.Content = task.Content + "\n\n[context note: " + summarizeDropped(dropped, counts) + "]"

	out := make([]llm.Message, 0, 1+keepPairs*2)
	out = append(out, task)
	out = append(out, msgs[tailStart:]...)
	return out
}

// summarizeDropped renders a deterministic (no-LLM) one-line summary of the
// tool calls in the dropped history.
func summarizeDropped(pairs int, counts map[string]int) string {
	var parts []string
	for _, name := range []string{"write_file", "read_file", "run_command", "search_code"} {
		if c := counts[name]; c > 0 {
			parts = append(parts, fmt.Sprintf("%s×%d", name, c))
		}
	}
	detail := strings.Join(parts, ", ")
	if detail == "" {
		detail = "various tools"
	}
	return fmt.Sprintf("omitted %d earlier tool interaction(s) (%s) to keep context bounded; re-read files with read_file if you need their current contents", pairs, detail)
}

// maxToolResultBytes caps a single tool result fed back to the model, so one
// verbose command (e.g. full go test output) can't blow up every later turn.
const maxToolResultBytes = 10000

// truncateMiddle keeps the head and tail of s, replacing the middle with a
// marker, when s exceeds max. The tail is preserved because compiler/test
// errors usually appear at the end.
func truncateMiddle(s string, max int) string {
	if len(s) <= max {
		return s
	}
	half := (max - 40) / 2
	if half < 0 {
		half = 0
	}
	return fmt.Sprintf("%s\n...[truncated %d bytes]...\n%s", s[:half], len(s)-2*half, s[len(s)-half:])
}

const toolLoopSystem = `You are a senior Go developer implementing a change in a fresh git sandbox workspace.
Use the tools to create/read files and run commands (e.g. "go build ./...", "go test ./...").
write_file replaces the ENTIRE file, so always write complete, compilable Go files.
Implement the requested change so the code builds and all tests pass. When it is complete and
"go test ./..." passes, STOP calling tools and reply with a one-paragraph summary of what you did.
Do not ask questions; make reasonable assumptions.`

// defaultToolLoopMaxTokens is the per-turn output cap when not overridden.
const defaultToolLoopMaxTokens = 8000

// LocalToolLoopAgent implements CodeAgent via an Anthropic tool-use loop over a sandbox.
type LocalToolLoopAgent struct {
	backend   llm.Backend
	model     string
	maxTurns  int
	maxTokens int
	searcher  ContextSearcher
}

// NewLocalToolLoopAgent wires the agent to an LLM backend + model.
func NewLocalToolLoopAgent(backend llm.Backend, model string) *LocalToolLoopAgent {
	return &LocalToolLoopAgent{backend: backend, model: model, maxTurns: maxToolTurns, maxTokens: defaultToolLoopMaxTokens}
}

// WithMaxTokens overrides the per-turn output-token cap (from config).
func (a *LocalToolLoopAgent) WithMaxTokens(n int) *LocalToolLoopAgent {
	if n > 0 {
		a.maxTokens = n
	}
	return a
}

// WithSearcher attaches a cross-repo searcher, enabling the search_code tool.
func (a *LocalToolLoopAgent) WithSearcher(s ContextSearcher) *LocalToolLoopAgent {
	a.searcher = s
	return a
}

func (a *LocalToolLoopAgent) toolDefs() []llm.ToolDef {
	strProp := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	defs := []llm.ToolDef{
		{Name: "write_file", Description: "Create or overwrite a file with the given full content.",
			InputSchema: map[string]any{"type": "object",
				"properties": map[string]any{"path": strProp("repo-relative path"), "content": strProp("full file content")},
				"required":   []string{"path", "content"}}},
		{Name: "read_file", Description: "Read a file's contents.",
			InputSchema: map[string]any{"type": "object",
				"properties": map[string]any{"path": strProp("repo-relative path")},
				"required":   []string{"path"}}},
		{Name: "run_command", Description: "Run a shell command in the workspace (e.g. go build ./..., go test ./...).",
			InputSchema: map[string]any{"type": "object",
				"properties": map[string]any{"command": strProp("shell command")},
				"required":   []string{"command"}}},
	}
	if a.searcher != nil {
		defs = append(defs, llm.ToolDef{Name: "search_code", Description: "Semantic search across the repositories for relevant code. Use this to understand existing/cross-repo context before and while implementing.",
			InputSchema: map[string]any{"type": "object",
				"properties": map[string]any{
					"query":    map[string]any{"type": "string", "description": "natural-language or code query"},
					"repo_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "restrict to these repos (optional)"},
					"k":        map[string]any{"type": "integer", "description": "max hits (default 8)"},
				}, "required": []string{"query"}}})
	}
	return defs
}

// Implement drives the model to implement the task in the sandbox.
func (a *LocalToolLoopAgent) Implement(ctx context.Context, task Task, sb sandbox.Sandbox) (Outcome, error) {
	messages := []llm.Message{{Role: "user", Content: buildPrompt(task)}}
	changed := map[string]bool{}
	var lastText string

	for turn := 0; turn < a.maxTurns; turn++ {
		// Abort promptly on cancellation/deadline (e.g. worker shutdown) rather
		// than feeding a canceled-context error back to the model as a tool
		// result — consistent with the pool/session transient-error handling.
		if err := ctx.Err(); err != nil {
			return Outcome{}, fmt.Errorf("codeagent: %w", err)
		}
		resp, err := a.backend.Complete(ctx, llm.Request{
			Model:     a.model,
			System:    skills.Augment(toolLoopSystem, "developer"),
			Messages:  truncateTranscript(messages, maxTranscriptPairs),
			Tools:     a.toolDefs(),
			MaxTokens: a.maxTokens,
		})
		if err != nil {
			return Outcome{}, fmt.Errorf("codeagent: complete: %w", err)
		}
		if resp.Text != "" {
			lastText = resp.Text
		}
		// Record the assistant turn (narration + any tool calls).
		messages = append(messages, llm.Message{Role: "assistant", Content: resp.Text, ToolCalls: resp.ToolCalls})
		if len(resp.ToolCalls) == 0 {
			// Model is done.
			return Outcome{Summary: lastText, FilesChanged: keys(changed)}, nil
		}
		results := make([]llm.ToolResult, 0, len(resp.ToolCalls))
		for _, call := range resp.ToolCalls {
			content, isErr := a.execTool(ctx, sb, call, changed)
			results = append(results, llm.ToolResult{ToolUseID: call.ID, Content: content, IsError: isErr})
		}
		messages = append(messages, llm.Message{Role: "user", ToolResults: results})
	}
	return Outcome{}, ErrToolLoopExceeded
}

// execTool runs one tool call against the sandbox and returns the result content
// for the model plus whether it was an error. It never returns a Go error — tool
// failures are reported back to the model so it can recover.
func (a *LocalToolLoopAgent) execTool(ctx context.Context, sb sandbox.Sandbox, call llm.ToolCall, changed map[string]bool) (string, bool) {
	switch call.Name {
	case "write_file":
		var in struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return fmt.Sprintf("invalid input: %v", err), true
		}
		if err := sb.WriteFile(in.Path, []byte(in.Content)); err != nil {
			return err.Error(), true
		}
		changed[in.Path] = true
		return "wrote " + in.Path, false
	case "read_file":
		var in struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return fmt.Sprintf("invalid input: %v", err), true
		}
		b, err := sb.ReadFile(in.Path)
		if err != nil {
			return err.Error(), true
		}
		return truncateMiddle(string(b), maxToolResultBytes), false
	case "run_command":
		var in struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return fmt.Sprintf("invalid input: %v", err), true
		}
		res, err := sb.Run(ctx, "sh", "-c", in.Command)
		if err != nil {
			return err.Error(), true
		}
		out := fmt.Sprintf("exit=%d\nstdout:\n%s\nstderr:\n%s",
			res.ExitCode, truncateMiddle(res.Stdout, maxToolResultBytes/2), truncateMiddle(res.Stderr, maxToolResultBytes/2))
		return out, res.ExitCode != 0
	case "search_code":
		if a.searcher == nil {
			return "search_code is not available", true
		}
		var in struct {
			Query   string   `json:"query"`
			RepoIDs []string `json:"repo_ids"`
			K       int      `json:"k"`
		}
		if err := json.Unmarshal(call.Input, &in); err != nil {
			return fmt.Sprintf("invalid input: %v", err), true
		}
		if in.K <= 0 {
			in.K = 8
		}
		hits, err := a.searcher.Search(ctx, in.Query, in.RepoIDs, in.K)
		if err != nil {
			return err.Error(), true
		}
		return formatHits(hits), false
	default:
		return "unknown tool: " + call.Name, true
	}
}

func formatHits(hits []SearchHit) string {
	if len(hits) == 0 {
		return "no results"
	}
	var b strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&b, "%s/%s (score %.2f)\n%s\n\n", h.RepoID, h.Path, h.Score, h.Snippet)
	}
	return strings.TrimSpace(b.String())
}

func buildPrompt(task Task) string {
	var b strings.Builder
	b.WriteString("Feature request:\n")
	b.WriteString(task.Spec)
	if len(task.Design) > 0 {
		b.WriteString("\n\nDesign plan (JSON):\n")
		b.Write(task.Design)
	}
	if task.Feedback != "" {
		b.WriteString("\n\nYour previous attempt failed deterministic QA. Fix these:\n")
		b.WriteString(task.Feedback)
	}
	if task.TargetRepoID != "" {
		b.WriteString("\n\nYou are implementing changes in repository: ")
		b.WriteString(task.TargetRepoID)
		b.WriteString("\nImplement only the part of the feature that belongs in this repository. Use search_code to understand the other repositories.")
	}
	if task.PriorRepoWork != "" {
		b.WriteString("\n\nAlready implemented in earlier repositories this run (keep your changes consistent with them):\n")
		b.WriteString(task.PriorRepoWork)
	}
	if len(task.RepoIDs) > 0 {
		b.WriteString("\n\nRepositories: ")
		b.WriteString(strings.Join(task.RepoIDs, ", "))
	}
	return b.String()
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
