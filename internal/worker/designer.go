package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fin/multi-agent-rag/internal/skills"
	"github.com/fin/multi-agent-rag/pkg/llm"
)

// FileChange is one file the developer must create/modify/delete.
// ComplexityHint drives per-file model routing in the developer stage.
type FileChange struct {
	FilePath       string `json:"file_path"`
	RepoID         string `json:"repo_id"`
	ChangeType     string `json:"change_type"`
	Description    string `json:"description"`
	ComplexityHint string `json:"complexity_hint"`
}

// TestCase is one test the developer must add to satisfy the design.
type TestCase struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	FilePath    string `json:"file_path"`
	Type        string `json:"type"`
}

// DesignArtifact is the system-designer's output: the concrete file-level plan
// the developer stage implements.
type DesignArtifact struct {
	FileChanges         []FileChange `json:"file_changes"`
	TestCases           []TestCase   `json:"test_cases"`
	ImplementationOrder string       `json:"implementation_order"`
	EdgeCases           []string     `json:"edge_cases"`
}

const designerModel = "claude-opus-4-8"

const designerSystem = `You are a system designer for a code-change pipeline.
Turn the request into a concrete file-level plan the developer will implement.
Reply with ONLY a JSON object (no prose, no code fences) of exactly this shape:
{"file_changes": [{"file_path": "<path>", "repo_id": "<repo>", "change_type": "modify|create|delete", "description": "<what changes>", "complexity_hint": "trivial|standard|complex"}],
 "test_cases": [{"name": "<TestName>", "description": "<what it verifies>", "file_path": "<test path>", "type": "unit|integration|e2e"}],
 "implementation_order": "<ordered prose of how to build it>",
 "edge_cases": ["<edge case>", ...]}
Provide at least one file change. Keep paths and names plausible for the stated repos.`

// SystemDesigner produces the file-level design via an Opus reasoning call.
type SystemDesigner struct {
	sm        *SessionManager
	model     string
	maxTokens int
	thinking  bool
	dir       RepoDirectory // optional; nil = no repo context grounding
}

// NewSystemDesigner wires the designer to a session manager (default params).
func NewSystemDesigner(sm *SessionManager) *SystemDesigner {
	p := paramsFor(RoleSystemDesigner)
	return &SystemDesigner{sm: sm, model: p.Model, maxTokens: p.MaxTokens, thinking: p.Thinking}
}

// WithParams overrides the role's model/max_tokens/thinking (from config).
func (h *SystemDesigner) WithParams(p RoleParams) *SystemDesigner {
	h.model, h.maxTokens, h.thinking = p.Model, p.MaxTokens, p.Thinking
	return h
}

// WithRepoDirectory injects each in-scope repo's context card into the prompt.
func (h *SystemDesigner) WithRepoDirectory(d RepoDirectory) *SystemDesigner {
	h.dir = d
	return h
}

func (h *SystemDesigner) Role() string { return "system-designer" }

// Handle produces the design artifact for the spec.
func (h *SystemDesigner) Handle(ctx context.Context, in StageInput) (StageResult, error) {
	user := fmt.Sprintf("Feature request:\n%s\n\nCandidate repositories: %v", in.Spec, in.RepoIDs)
	user = appendRepoContext(ctx, h.dir, user, in.RepoIDs)
	art, usage, err := decodeReply(ctx, h.sm, llm.Request{
		Model:     h.model,
		System:    skills.Augment(designerSystem, "system-designer"),
		Messages:  []llm.Message{{Role: "user", Content: user}},
		MaxTokens: h.maxTokens,
		Thinking:  h.thinking,
	}, func(text string) (DesignArtifact, error) {
		var a DesignArtifact
		if err := json.Unmarshal([]byte(extractJSONObject(text)), &a); err != nil {
			return a, fmt.Errorf("parse reply: %w", err)
		}
		if len(a.FileChanges) == 0 {
			return a, fmt.Errorf("%w: system-designer produced no file changes", ErrBadArtifact)
		}
		return a, nil
	})
	if err != nil {
		return StageResult{}, fmt.Errorf("system-designer: %w", err)
	}

	out, err := json.Marshal(art)
	if err != nil {
		return StageResult{}, fmt.Errorf("system-designer: marshal artifact: %w", err)
	}
	return StageResult{Stage: in.Stage, ArtifactJSON: out, Usage: usage}, nil
}
