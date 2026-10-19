package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/fin/multi-agent-rag/internal/skills"
	"github.com/fin/multi-agent-rag/pkg/llm"
)

// ErrNoImplementation marks a qa run with no developer artifact to review.
var ErrNoImplementation = errors.New("qa: no developer artifact to review")

// QAArtifact is the semantic QA verdict. qa_passed is THE gate the orchestrator
// consumes (false loops back to developing).
type QAArtifact struct {
	QAPassed  bool     `json:"qa_passed"`
	Issues    []string `json:"issues"`
	Rationale string   `json:"rationale"`
}

// qaSchema constrains the reply to the QAArtifact shape.
var qaSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"qa_passed": map[string]any{"type": "boolean"},
		"issues":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"rationale": map[string]any{"type": "string"},
	},
	"required":             []string{"qa_passed"},
	"additionalProperties": false,
}

const qaModel = "claude-sonnet-5"

const qaSystem = `You are a QA reviewer deciding whether an implementation satisfies a feature request.
Deterministic build/vet/test checks have already passed; judge SPEC COMPLIANCE and completeness only.
Reply with ONLY a JSON object (no prose, no code fences) of exactly this shape:
{"qa_passed": true|false, "issues": ["<issue>", ...], "rationale": "<1-2 sentences>"}
Pass only if the implementation plausibly and completely satisfies the request.`

// QA is the semantic spec-compliance gate.
type QA struct {
	sm        *SessionManager
	model     string
	maxTokens int
	thinking  bool
}

// NewQA wires the reviewer to a session manager.
func NewQA(sm *SessionManager) *QA {
	p := paramsFor(RoleQA)
	return &QA{sm: sm, model: p.Model, maxTokens: p.MaxTokens, thinking: p.Thinking}
}

// WithParams overrides the role's model/max_tokens/thinking (from config).
func (h *QA) WithParams(p RoleParams) *QA {
	h.model, h.maxTokens, h.thinking = p.Model, p.MaxTokens, p.Thinking
	return h
}

func (h *QA) Role() string { return "qa" }

// Handle reviews the developer's output against the spec and emits a QAArtifact.
func (h *QA) Handle(ctx context.Context, in StageInput) (StageResult, error) {
	devJSON, ok := in.PriorArtifacts["developing"]
	if !ok || len(devJSON) == 0 {
		return StageResult{}, ErrNoImplementation
	}
	var dev DeveloperArtifact
	if err := json.Unmarshal(devJSON, &dev); err != nil {
		return StageResult{}, fmt.Errorf("qa: parse developer artifact: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Feature request:\n%s\n\n", in.Spec)
	if len(dev.Repos) > 0 {
		fmt.Fprintf(&b, "Implementation spans %d repositor(y/ies):\n", len(dev.Repos))
		for _, r := range dev.Repos {
			fmt.Fprintf(&b, "- %s: deterministic tests passed=%t, files=%v", r.RepoID, r.TestsPassed, r.FilesChanged)
			if r.Failure != "" {
				fmt.Fprintf(&b, ", failure=%s", r.Failure)
			}
			b.WriteString("\n")
		}
		if dev.Summary != "" {
			fmt.Fprintf(&b, "\nOverall summary: %s\n", dev.Summary)
		}
	} else {
		fmt.Fprintf(&b, "Implementation summary:\n%s\nFiles changed: %v\nDeterministic tests passed: %t",
			dev.Summary, dev.FilesChanged, dev.TestsPassed)
	}
	user := b.String()

	art, usage, err := decodeReply(ctx, h.sm, llm.Request{
		Model:        h.model,
		System:       skills.Augment(qaSystem, "qa"),
		Messages:     []llm.Message{{Role: "user", Content: user}},
		MaxTokens:    h.maxTokens,
		Thinking:     h.thinking,
		OutputSchema: qaSchema,
	}, func(text string) (QAArtifact, error) {
		var a QAArtifact
		if err := json.Unmarshal([]byte(extractJSONObject(text)), &a); err != nil {
			return a, fmt.Errorf("parse reply: %w", err)
		}
		return a, nil
	})
	if err != nil {
		return StageResult{}, fmt.Errorf("qa: %w", err)
	}
	out, err := json.Marshal(art)
	if err != nil {
		return StageResult{}, fmt.Errorf("qa: marshal artifact: %w", err)
	}
	return StageResult{Stage: in.Stage, ArtifactJSON: out, Usage: usage}, nil
}
