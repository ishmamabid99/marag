package worker

import (
	"context"
	"testing"

	"github.com/fin/multi-agent-rag/pkg/llm"
)

func TestDefaultRosterComplete(t *testing.T) {
	r := DefaultRoster()
	roles := []string{
		RoleRequirementAnalyzer, RoleViabilityChecker, RoleRiskAnalyzer,
		RoleSystemArchitect, RoleSystemDesigner, RoleQA,
		RoleDeveloper, RoleDeveloperEscalation, RoleDeveloperCheap,
	}
	for _, role := range roles {
		p := r[role]
		if p.Model == "" || p.MaxTokens == 0 {
			t.Fatalf("default roster incomplete for %q: %+v", role, p)
		}
	}
	// Preserve the documented tiering defaults.
	if r[RoleRequirementAnalyzer].Model != "claude-haiku-4-5" || r[RoleSystemArchitect].Model != "claude-opus-4-8" {
		t.Fatalf("unexpected default models: %+v", r)
	}
	if !r[RoleSystemArchitect].Thinking || r[RoleRequirementAnalyzer].Thinking {
		t.Fatalf("thinking defaults wrong: architect=%v analyzer=%v",
			r[RoleSystemArchitect].Thinking, r[RoleRequirementAnalyzer].Thinking)
	}
}

func TestRoleWithParamsFlowsToRequest(t *testing.T) {
	be := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: `{"decisions":[{"decision":"d","rationale":"r","scope":"s"}],"components":[],"new_interfaces":[],"integration_points":[],"migration_strategy":""}`}, nil
	})
	h := NewSystemArchitect(NewSessionManager(be, SessionConfig{})).
		WithParams(RoleParams{Model: "claude-haiku-4-5", MaxTokens: 4321, Thinking: false})
	if _, err := h.Handle(context.Background(), StageInput{Stage: "architecting", Spec: "x", RepoIDs: []string{"r"}}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	c := be.Calls()[0]
	if c.Model != "claude-haiku-4-5" || c.MaxTokens != 4321 || c.Thinking {
		t.Fatalf("WithParams not applied to request: model=%s max=%d thinking=%v", c.Model, c.MaxTokens, c.Thinking)
	}
}
