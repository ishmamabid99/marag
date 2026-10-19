package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fin/multi-agent-rag/internal/skills"
	"github.com/fin/multi-agent-rag/pkg/llm"
)

// AffectedFlow is one execution flow the change touches (RiskArtifact field).
type AffectedFlow struct {
	FlowName    string   `json:"flow_name"`
	RepoID      string   `json:"repo_id"`
	EntryPoints []string `json:"entry_points"`
	ImpactDesc  string   `json:"impact_desc"`
}

// RiskArtifact is the risk-analyzer's output: blast radius + breaking risks +
// a gate recommendation the orchestrator will later use to park for humans.
type RiskArtifact struct {
	BlastRadius     string         `json:"blast_radius"`
	AffectedFlows   []AffectedFlow `json:"affected_flows"`
	BreakingRisks   []string       `json:"breaking_risks"`
	GateRecommended bool           `json:"gate_recommended"`
	GateReason      string         `json:"gate_reason"`
}

const riskModel = "claude-sonnet-5"

const riskSystem = `You are a risk analyzer for a code-change pipeline.
Assess the blast radius of the feature request and whether a human gate is warranted.
Reply with ONLY a JSON object (no prose, no code fences) of exactly this shape:
{"blast_radius": "low|medium|high|critical",
 "affected_flows": [{"flow_name": "<name>", "repo_id": "<repo>", "entry_points": ["<entry>"], "impact_desc": "<what changes>"}],
 "breaking_risks": ["<risk>", ...],
 "gate_recommended": true|false,
 "gate_reason": "<why a human should review, or empty>"}
Blast radius guide: low = isolated/additive; medium = one subsystem; high = multiple
subsystems or public API; critical = data migration, auth, or money paths. Recommend a
gate when the change is high/critical or touches security, data, or money.`

// RiskAnalyzer estimates blast radius via a single Sonnet reasoning call.
type RiskAnalyzer struct {
	sm        *SessionManager
	model     string
	maxTokens int
	thinking  bool
}

// NewRiskAnalyzer wires the analyzer to a session manager.
func NewRiskAnalyzer(sm *SessionManager) *RiskAnalyzer {
	p := paramsFor(RoleRiskAnalyzer)
	return &RiskAnalyzer{sm: sm, model: p.Model, maxTokens: p.MaxTokens, thinking: p.Thinking}
}

// WithParams overrides the role's model/max_tokens/thinking (from config).
func (h *RiskAnalyzer) WithParams(p RoleParams) *RiskAnalyzer {
	h.model, h.maxTokens, h.thinking = p.Model, p.MaxTokens, p.Thinking
	return h
}

func (h *RiskAnalyzer) Role() string { return "risk-analyzer" }

// Handle assesses blast radius + gate recommendation for the spec.
func (h *RiskAnalyzer) Handle(ctx context.Context, in StageInput) (StageResult, error) {
	user := fmt.Sprintf("Feature request:\n%s\n\nCandidate repositories: %v", in.Spec, in.RepoIDs)
	art, usage, err := decodeReply(ctx, h.sm, llm.Request{
		Model:     h.model,
		System:    skills.Augment(riskSystem, "risk-analyzer"),
		Messages:  []llm.Message{{Role: "user", Content: user}},
		MaxTokens: h.maxTokens,
		Thinking:  h.thinking,
	}, func(text string) (RiskArtifact, error) {
		var a RiskArtifact
		if err := json.Unmarshal([]byte(extractJSONObject(text)), &a); err != nil {
			return a, fmt.Errorf("parse reply: %w", err)
		}
		switch a.BlastRadius {
		case "low", "medium", "high", "critical":
		default:
			return a, fmt.Errorf("%w: risk-analyzer blast_radius %q", ErrBadArtifact, a.BlastRadius)
		}
		return a, nil
	})
	if err != nil {
		return StageResult{}, fmt.Errorf("risk-analyzer: %w", err)
	}

	out, err := json.Marshal(art)
	if err != nil {
		return StageResult{}, fmt.Errorf("risk-analyzer: marshal artifact: %w", err)
	}
	return StageResult{Stage: in.Stage, ArtifactJSON: out, Usage: usage}, nil
}
