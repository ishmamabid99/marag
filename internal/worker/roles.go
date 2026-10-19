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

// ErrBadClassification marks an unparseable or invalid analyzer reply.
var ErrBadClassification = errors.New("requirement-analyzer: invalid classification")

// ErrBadArtifact marks a role reply that parsed but failed validation
// (missing required content or an invalid enum). Terminal: a retry with the
// same input will not fix it.
var ErrBadArtifact = errors.New("role: invalid artifact")

// StageInput is the input to a role handler.
type StageInput struct {
	RunID   string
	Stage   string
	Spec    string
	RepoIDs []string
	Attempt int

	PriorArtifacts map[string][]byte // upstream stage artifacts, keyed by stage
}

// StageResult is a role handler's output.
type StageResult struct {
	Stage        string
	ArtifactJSON []byte
	Usage        llm.Usage
}

// StageHandler processes one stage for a run.
type StageHandler interface {
	Role() string
	Handle(ctx context.Context, in StageInput) (StageResult, error)
}

// RequirementArtifact is the requirement-analyzer's output.
type RequirementArtifact struct {
	Summary               string   `json:"summary"`
	PreliminaryComplexity string   `json:"preliminary_complexity"`
	AffectedRepos         []string `json:"affected_repos"`
	KeyRequirements       []string `json:"key_requirements"`
	// ProposedRepos names repos NOT in the run's candidate set that the feature
	// also appears to need. It is a proposal for human approval (propose, don't
	// act) — never an assumption that those repos will be changed.
	ProposedRepos []string `json:"proposed_repos"`
}

// reqAnalyzerSchema constrains the reply to the RequirementArtifact shape
// (structured outputs); mirrors the json tags above exactly.
var reqAnalyzerSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"summary":                map[string]any{"type": "string"},
		"preliminary_complexity": map[string]any{"type": "string", "enum": []string{"trivial", "standard", "complex"}},
		"affected_repos":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"key_requirements":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"proposed_repos":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	},
	// affected_repos + proposed_repos are REQUIRED so a weak model (Haiku) must
	// populate them rather than emitting a minimal artifact with null arrays,
	// which would make repo grounding + the propose-don't-act gate inert.
	"required":             []string{"preliminary_complexity", "affected_repos", "proposed_repos"},
	"additionalProperties": false,
}

const reqAnalyzerModel = "claude-haiku-4-5"

const reqAnalyzerSystem = `You are a requirement analyzer for a code-change pipeline.
Classify the feature request's complexity and identify affected repositories.
Reply with ONLY a JSON object (no prose, no code fences) of exactly this shape:
{"summary": "<1-2 sentence summary>",
 "preliminary_complexity": "trivial|standard|complex",
 "affected_repos": ["<repo-id>", ...],
 "key_requirements": ["<requirement>", ...],
 "proposed_repos": ["<repo-id>", ...]}
Complexity guide: trivial = one-line/config fix; standard = localized feature in one area;
complex = cross-repo or architectural change.
Set affected_repos to the candidate repositories that must change. If a repository NOT
in the candidate list is clearly required, name it in proposed_repos — this is only a
proposal for human approval, never an assumption that it will be changed.`

// RequirementAnalyzer classifies a spec via a single Haiku Messages call.
type RequirementAnalyzer struct {
	sm        *SessionManager
	model     string
	maxTokens int
	thinking  bool
	dir       RepoDirectory // optional; nil = no cross-repo grounding
}

// WithRepoDirectory grounds repo selection in the registered repos and their
// context cards. Without it, the analyzer classifies from the spec alone.
func (h *RequirementAnalyzer) WithRepoDirectory(d RepoDirectory) *RequirementAnalyzer {
	h.dir = d
	return h
}

// NewRequirementAnalyzer wires the analyzer to a session manager.
func NewRequirementAnalyzer(sm *SessionManager) *RequirementAnalyzer {
	p := paramsFor(RoleRequirementAnalyzer)
	return &RequirementAnalyzer{sm: sm, model: p.Model, maxTokens: p.MaxTokens, thinking: p.Thinking}
}

// WithParams overrides the role's model/max_tokens/thinking (from config).
func (h *RequirementAnalyzer) WithParams(p RoleParams) *RequirementAnalyzer {
	h.model, h.maxTokens, h.thinking = p.Model, p.MaxTokens, p.Thinking
	return h
}

func (h *RequirementAnalyzer) Role() string { return "requirement-analyzer" }

// Handle classifies the spec and returns a marshaled RequirementArtifact.
func (h *RequirementAnalyzer) Handle(ctx context.Context, in StageInput) (StageResult, error) {
	user := fmt.Sprintf("Feature request:\n%s\n\nCandidate repositories: %v", in.Spec, in.RepoIDs)
	if h.dir != nil {
		if repos, err := h.dir.List(ctx, 300); err == nil {
			user += "\n\n" + FormatDirectory(repos) +
				"\n\nUsing this directory as evidence, you MUST evaluate every registered repository against the request:\n" +
				"- Populate affected_repos with the CANDIDATE repos that must change (may be empty if none fit).\n" +
				"- If the request clearly belongs to a repository that is NOT in the candidate list, you MUST list that repo id in proposed_repos. This is a proposal for human approval, never an assumption it will change.\n" +
				"Do not leave these fields null."
		}
	}
	art, usage, err := decodeReply(ctx, h.sm, llm.Request{
		Model:        h.model,
		Thinking:     h.thinking,
		System:       skills.Augment(reqAnalyzerSystem, "requirement-analyzer"),
		Messages:     []llm.Message{{Role: "user", Content: user}},
		MaxTokens:    h.maxTokens,
		OutputSchema: reqAnalyzerSchema,
	}, func(text string) (RequirementArtifact, error) {
		var a RequirementArtifact
		if err := json.Unmarshal([]byte(extractJSONObject(text)), &a); err != nil {
			return a, fmt.Errorf("parse reply: %w", err)
		}
		switch a.PreliminaryComplexity {
		case "trivial", "standard", "complex":
		default:
			return a, fmt.Errorf("%w: complexity %q", ErrBadClassification, a.PreliminaryComplexity)
		}
		return a, nil
	})
	if err != nil {
		return StageResult{}, fmt.Errorf("requirement-analyzer: %w", err)
	}

	out, err := json.Marshal(art)
	if err != nil {
		return StageResult{}, fmt.Errorf("requirement-analyzer: marshal artifact: %w", err)
	}
	return StageResult{Stage: in.Stage, ArtifactJSON: out, Usage: usage}, nil
}

// extractJSONObject returns the JSON object embedded in s, tolerating code
// fences or surrounding prose by slicing from the first '{' to the last '}'.
func extractJSONObject(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < 0 || end < start {
		return s // no braces; let json.Unmarshal produce the error
	}
	return s[start : end+1]
}
