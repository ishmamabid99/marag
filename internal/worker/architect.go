package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fin/multi-agent-rag/internal/skills"
	"github.com/fin/multi-agent-rag/pkg/llm"
)

// ArchDecision is one architectural decision (ArchitectureArtifact field).
type ArchDecision struct {
	Decision  string `json:"decision"`
	Rationale string `json:"rationale"`
	Scope     string `json:"scope"`
}

// ComponentChange describes a component the change adds/modifies/deprecates.
type ComponentChange struct {
	Component   string `json:"component"`
	RepoID      string `json:"repo_id"`
	ChangeType  string `json:"change_type"`
	Description string `json:"description"`
}

// ArchitectureArtifact is the system-architect's output: the structural plan
// the designer turns into concrete file changes.
type ArchitectureArtifact struct {
	Decisions         []ArchDecision    `json:"decisions"`
	Components        []ComponentChange `json:"components"`
	NewInterfaces     []string          `json:"new_interfaces"`
	IntegrationPoints []string          `json:"integration_points"`
	MigrationStrategy string            `json:"migration_strategy"`
}

const architectModel = "claude-opus-4-8"

const architectSystem = `You are a system architect for a code-change pipeline.
Produce the structural plan for the feature: the key decisions, the components that
change, new interfaces, integration points, and any migration strategy.
Reply with ONLY a JSON object (no prose, no code fences) of exactly this shape:
{"decisions": [{"decision": "<what>", "rationale": "<why>", "scope": "<component/service>"}],
 "components": [{"component": "<name>", "repo_id": "<repo>", "change_type": "modify|create|deprecate", "description": "<what changes>"}],
 "new_interfaces": ["<signature or interface>", ...],
 "integration_points": ["<where components connect>", ...],
 "migration_strategy": "<how to roll out safely, or empty if none>"}
Provide at least one decision. Keep it grounded in the stated request; do not invent
scope beyond it.`

// SystemArchitect produces the architecture plan via an Opus reasoning call.
type SystemArchitect struct {
	sm        *SessionManager
	model     string
	maxTokens int
	thinking  bool
	dir       RepoDirectory // optional; nil = no repo context grounding
}

// NewSystemArchitect wires the architect to a session manager (default params).
func NewSystemArchitect(sm *SessionManager) *SystemArchitect {
	p := paramsFor(RoleSystemArchitect)
	return &SystemArchitect{sm: sm, model: p.Model, maxTokens: p.MaxTokens, thinking: p.Thinking}
}

// WithParams overrides the role's model/max_tokens/thinking (from config).
func (h *SystemArchitect) WithParams(p RoleParams) *SystemArchitect {
	h.model, h.maxTokens, h.thinking = p.Model, p.MaxTokens, p.Thinking
	return h
}

// WithRepoDirectory injects each in-scope repo's context card into the prompt.
func (h *SystemArchitect) WithRepoDirectory(d RepoDirectory) *SystemArchitect {
	h.dir = d
	return h
}

func (h *SystemArchitect) Role() string { return "system-architect" }

// Handle produces the architecture artifact for the spec.
func (h *SystemArchitect) Handle(ctx context.Context, in StageInput) (StageResult, error) {
	user := fmt.Sprintf("Feature request:\n%s\n\nCandidate repositories: %v", in.Spec, in.RepoIDs)
	user = appendRepoContext(ctx, h.dir, user, in.RepoIDs)
	art, usage, err := decodeReply(ctx, h.sm, llm.Request{
		Model:     h.model,
		System:    skills.Augment(architectSystem, "system-architect"),
		Messages:  []llm.Message{{Role: "user", Content: user}},
		MaxTokens: h.maxTokens,
		Thinking:  h.thinking,
	}, func(text string) (ArchitectureArtifact, error) {
		var a ArchitectureArtifact
		if err := json.Unmarshal([]byte(extractJSONObject(text)), &a); err != nil {
			return a, fmt.Errorf("parse reply: %w", err)
		}
		if len(a.Decisions) == 0 {
			return a, fmt.Errorf("%w: system-architect produced no decisions", ErrBadArtifact)
		}
		return a, nil
	})
	if err != nil {
		return StageResult{}, fmt.Errorf("system-architect: %w", err)
	}

	out, err := json.Marshal(art)
	if err != nil {
		return StageResult{}, fmt.Errorf("system-architect: marshal artifact: %w", err)
	}
	return StageResult{Stage: in.Stage, ArtifactJSON: out, Usage: usage}, nil
}
