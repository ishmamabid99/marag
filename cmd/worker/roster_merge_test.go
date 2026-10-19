package main

import (
	"testing"

	"github.com/fin/multi-agent-rag/internal/config"
	"github.com/fin/multi-agent-rag/internal/worker"
)

func TestResolveRosterMergesOverrides(t *testing.T) {
	tru := true
	r := resolveRoster(map[string]config.RoleConfig{
		worker.RoleSystemArchitect:     {MaxTokens: 12000},                          // only max_tokens
		worker.RoleRequirementAnalyzer: {Model: "claude-sonnet-5", Thinking: &tru},  // model + thinking
	})

	// Architect: default model kept, max_tokens overridden, default thinking kept.
	a := r[worker.RoleSystemArchitect]
	if a.Model != "claude-opus-4-8" || a.MaxTokens != 12000 || !a.Thinking {
		t.Fatalf("architect merge wrong: %+v", a)
	}
	// Analyzer: model + thinking overridden, max_tokens default (1000).
	an := r[worker.RoleRequirementAnalyzer]
	if an.Model != "claude-sonnet-5" || !an.Thinking || an.MaxTokens != 1000 {
		t.Fatalf("analyzer merge wrong: %+v", an)
	}
	// Untouched role keeps its defaults.
	if q := r[worker.RoleQA]; q.Model != "claude-sonnet-5" || q.MaxTokens != 1500 {
		t.Fatalf("qa should keep defaults: %+v", q)
	}
}
