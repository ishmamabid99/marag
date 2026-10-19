package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/fin/multi-agent-rag/internal/skills"
	"github.com/fin/multi-agent-rag/pkg/llm"
)

// ErrInfeasible marks a run the viability-checker assessed as not feasible.
// Interim behavior (pre-gates): a terminal failure that stops the run.
var ErrInfeasible = errors.New("viability-checker: run assessed infeasible")

// ViabilityArtifact is the viability-checker's output. confirmed_complexity is
// THE routing decision consumed by the orchestrator.
type ViabilityArtifact struct {
	Feasible             bool     `json:"feasible"`
	ConfirmedComplexity  string   `json:"confirmed_complexity"`
	FeasibilityRationale string   `json:"feasibility_rationale"`
	RisksIdentified      []string `json:"risks_identified"`
	Constraints          []string `json:"constraints"`
}

// viabilitySchema constrains the reply to the ViabilityArtifact shape.
var viabilitySchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"feasible":              map[string]any{"type": "boolean"},
		"confirmed_complexity":  map[string]any{"type": "string", "enum": []string{"trivial", "standard", "complex"}},
		"feasibility_rationale": map[string]any{"type": "string"},
		"risks_identified":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"constraints":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	},
	"required":             []string{"feasible", "confirmed_complexity"},
	"additionalProperties": false,
}

const viabilityModel = "claude-sonnet-5"

const viabilitySystem = `You are a viability checker for a code-change pipeline.
Decide whether the feature request is feasible, and confirm its complexity class
after weighing the whole request.
Reply with ONLY a JSON object (no prose, no code fences) of exactly this shape:
{"feasible": true|false,
 "confirmed_complexity": "trivial|standard|complex",
 "feasibility_rationale": "<1-2 sentences>",
 "risks_identified": ["<risk>", ...],
 "constraints": ["<constraint>", ...]}
Complexity guide: trivial = one-line/config fix; standard = localized feature;
complex = cross-repo or architectural change. Mark feasible=false only when the
request cannot be implemented as stated (e.g. depends on something that does not exist).`

// ViabilityChecker validates feasibility and confirms complexity via Sonnet.
type ViabilityChecker struct {
	sm        *SessionManager
	model     string
	maxTokens int
	thinking  bool
}

// NewViabilityChecker wires the checker to a session manager.
func NewViabilityChecker(sm *SessionManager) *ViabilityChecker {
	p := paramsFor(RoleViabilityChecker)
	return &ViabilityChecker{sm: sm, model: p.Model, maxTokens: p.MaxTokens, thinking: p.Thinking}
}

// WithParams overrides the role's model/max_tokens/thinking (from config).
func (h *ViabilityChecker) WithParams(p RoleParams) *ViabilityChecker {
	h.model, h.maxTokens, h.thinking = p.Model, p.MaxTokens, p.Thinking
	return h
}

func (h *ViabilityChecker) Role() string { return "viability-checker" }

// Handle assesses feasibility + confirmed complexity for the spec.
func (h *ViabilityChecker) Handle(ctx context.Context, in StageInput) (StageResult, error) {
	user := fmt.Sprintf("Feature request:\n%s\n\nCandidate repositories: %v", in.Spec, in.RepoIDs)
	art, usage, err := decodeReply(ctx, h.sm, llm.Request{
		Model:        h.model,
		Thinking:     h.thinking,
		System:       skills.Augment(viabilitySystem, "viability-checker"),
		Messages:     []llm.Message{{Role: "user", Content: user}},
		MaxTokens:    h.maxTokens,
		OutputSchema: viabilitySchema,
	}, func(text string) (ViabilityArtifact, error) {
		var a ViabilityArtifact
		if err := json.Unmarshal([]byte(extractJSONObject(text)), &a); err != nil {
			return a, fmt.Errorf("parse reply: %w", err)
		}
		switch a.ConfirmedComplexity {
		case "trivial", "standard", "complex":
		default:
			return a, fmt.Errorf("%w: complexity %q", ErrBadClassification, a.ConfirmedComplexity)
		}
		return a, nil
	})
	if err != nil {
		return StageResult{}, fmt.Errorf("viability-checker: %w", err)
	}
	// Infeasibility is a valid, well-formed verdict (not a contract error) — it
	// terminates the run rather than triggering a re-prompt.
	if !art.Feasible {
		return StageResult{}, fmt.Errorf("%w: %s", ErrInfeasible, art.FeasibilityRationale)
	}

	out, err := json.Marshal(art)
	if err != nil {
		return StageResult{}, fmt.Errorf("viability-checker: marshal artifact: %w", err)
	}
	return StageResult{Stage: in.Stage, ArtifactJSON: out, Usage: usage}, nil
}
