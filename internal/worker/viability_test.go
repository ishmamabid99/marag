package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fin/multi-agent-rag/pkg/llm"
)

func TestViabilityCheckerConfirmsComplexity(t *testing.T) {
	reply := `{"feasible":true,"confirmed_complexity":"complex","feasibility_rationale":"cross-repo change","risks_identified":["schema migration"],"constraints":["backward compatible"]}`
	backend := llm.NewFake(func(_ context.Context, req llm.Request) (llm.Response, error) {
		if req.Model != "claude-sonnet-5" {
			t.Errorf("model = %q, want claude-sonnet-5", req.Model)
		}
		if !strings.Contains(req.Messages[0].Content, "add cross-repo feature") {
			t.Errorf("spec not in prompt: %q", req.Messages[0].Content)
		}
		return llm.Response{Text: reply, Usage: llm.Usage{InputTokens: 80, OutputTokens: 40}}, nil
	})
	h := NewViabilityChecker(NewSessionManager(backend, SessionConfig{}))
	if h.Role() != "viability-checker" {
		t.Fatalf("Role = %q", h.Role())
	}
	res, err := h.Handle(context.Background(), StageInput{RunID: "r1", Stage: "viability", Spec: "add cross-repo feature", Attempt: 1})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Usage.OutputTokens != 40 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	var art ViabilityArtifact
	if err := json.Unmarshal(res.ArtifactJSON, &art); err != nil {
		t.Fatalf("artifact json: %v", err)
	}
	if !art.Feasible || art.ConfirmedComplexity != "complex" {
		t.Fatalf("artifact = %+v", art)
	}
}

func TestViabilityCheckerInfeasible(t *testing.T) {
	backend := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: `{"feasible":false,"confirmed_complexity":"complex","feasibility_rationale":"requires a nonexistent external API"}`}, nil
	})
	h := NewViabilityChecker(NewSessionManager(backend, SessionConfig{}))
	_, err := h.Handle(context.Background(), StageInput{RunID: "r", Stage: "viability", Spec: "x", Attempt: 1})
	if !errors.Is(err, ErrInfeasible) {
		t.Fatalf("err = %v, want ErrInfeasible", err)
	}
}

func TestViabilityCheckerBadComplexity(t *testing.T) {
	backend := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: `{"feasible":true,"confirmed_complexity":"epic"}`}, nil
	})
	h := NewViabilityChecker(NewSessionManager(backend, SessionConfig{}))
	_, err := h.Handle(context.Background(), StageInput{RunID: "r", Stage: "viability", Spec: "x", Attempt: 1})
	if !errors.Is(err, ErrBadClassification) {
		t.Fatalf("err = %v, want ErrBadClassification", err)
	}
}

func TestViabilityCheckerStripsCodeFences(t *testing.T) {
	backend := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: "```json\n{\"feasible\":true,\"confirmed_complexity\":\"standard\"}\n```"}, nil
	})
	h := NewViabilityChecker(NewSessionManager(backend, SessionConfig{}))
	res, err := h.Handle(context.Background(), StageInput{RunID: "r", Stage: "viability", Spec: "x", Attempt: 1})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var art ViabilityArtifact
	_ = json.Unmarshal(res.ArtifactJSON, &art)
	if art.ConfirmedComplexity != "standard" {
		t.Fatalf("complexity = %q", art.ConfirmedComplexity)
	}
}
