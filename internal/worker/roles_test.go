package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fin/multi-agent-rag/pkg/llm"
)

func TestRequirementAnalyzerClassifies(t *testing.T) {
	reply := `{"summary":"add currency validation","preliminary_complexity":"standard","affected_repos":["payment-svc"],"key_requirements":["validate EUR/GBP"]}`
	backend := llm.NewFake(func(_ context.Context, req llm.Request) (llm.Response, error) {
		if req.Model != "claude-haiku-4-5" {
			t.Errorf("model = %q, want claude-haiku-4-5", req.Model)
		}
		if !strings.Contains(req.Messages[0].Content, "add currency validation") {
			t.Errorf("spec not in prompt: %q", req.Messages[0].Content)
		}
		return llm.Response{Text: reply, Usage: llm.Usage{InputTokens: 40, OutputTokens: 20}}, nil
	})
	h := NewRequirementAnalyzer(NewSessionManager(backend, SessionConfig{}))
	if h.Role() != "requirement-analyzer" {
		t.Fatalf("Role = %q", h.Role())
	}

	res, err := h.Handle(context.Background(), StageInput{
		RunID: "r1", Stage: "analyzing", Spec: "add currency validation", RepoIDs: []string{"payment-svc"}, Attempt: 1,
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.Usage.OutputTokens != 20 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	var art RequirementArtifact
	if err := json.Unmarshal(res.ArtifactJSON, &art); err != nil {
		t.Fatalf("artifact json: %v", err)
	}
	if art.PreliminaryComplexity != "standard" || len(art.AffectedRepos) != 1 {
		t.Fatalf("artifact = %+v", art)
	}
}

func TestRequirementAnalyzerStripsCodeFences(t *testing.T) {
	reply := "```json\n{\"summary\":\"s\",\"preliminary_complexity\":\"standard\",\"affected_repos\":[]}\n```"
	backend := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: reply}, nil
	})
	h := NewRequirementAnalyzer(NewSessionManager(backend, SessionConfig{}))
	res, err := h.Handle(context.Background(), StageInput{RunID: "r", Spec: "x", Attempt: 1})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var art RequirementArtifact
	if err := json.Unmarshal(res.ArtifactJSON, &art); err != nil {
		t.Fatalf("artifact json: %v", err)
	}
	if art.PreliminaryComplexity != "standard" {
		t.Fatalf("PreliminaryComplexity = %q, want standard", art.PreliminaryComplexity)
	}
}

func TestRequirementAnalyzerRejectsBadJSON(t *testing.T) {
	backend := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: "not json"}, nil
	})
	h := NewRequirementAnalyzer(NewSessionManager(backend, SessionConfig{}))
	if _, err := h.Handle(context.Background(), StageInput{RunID: "r", Spec: "x", Attempt: 1}); err == nil {
		t.Fatal("expected error on non-JSON reply")
	}
}

// fakeDir is a test RepoDirectory shared across worker role tests.
type fakeDir struct {
	repos []RepoInfo
	cards map[string]string
}

func (f fakeDir) List(_ context.Context, _ int) ([]RepoInfo, error) { return f.repos, nil }
func (f fakeDir) Card(_ context.Context, id string) (string, error) { return f.cards[id], nil }

func TestAnalyzerGroundsAndProposesRepos(t *testing.T) {
	dir := fakeDir{repos: []RepoInfo{
		{ID: "payments", Name: "p"},
		{ID: "ledger", Name: "l", Excerpt: "double-entry ledger"},
	}}
	var sawDirectory bool
	be := llm.NewFake(func(_ context.Context, req llm.Request) (llm.Response, error) {
		if strings.Contains(req.Messages[0].Content, "double-entry ledger") {
			sawDirectory = true
		}
		return llm.Response{Text: `{"summary":"s","preliminary_complexity":"standard","affected_repos":["payments"],"proposed_repos":["ledger"]}`}, nil
	})
	h := NewRequirementAnalyzer(NewSessionManager(be, SessionConfig{})).WithRepoDirectory(dir)
	res, err := h.Handle(context.Background(), StageInput{
		Stage: "analyzing", Spec: "record a payment in the ledger", RepoIDs: []string{"payments"}, Attempt: 1,
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !sawDirectory {
		t.Fatal("analyzer prompt did not include the repo directory")
	}
	var art RequirementArtifact
	if err := json.Unmarshal(res.ArtifactJSON, &art); err != nil {
		t.Fatalf("artifact json: %v", err)
	}
	if len(art.ProposedRepos) != 1 || art.ProposedRepos[0] != "ledger" {
		t.Fatalf("proposed_repos=%v want [ledger]", art.ProposedRepos)
	}
}

func TestRequirementAnalyzerRejectsBadComplexity(t *testing.T) {
	backend := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: `{"summary":"x","preliminary_complexity":"huge","affected_repos":[]}`}, nil
	})
	h := NewRequirementAnalyzer(NewSessionManager(backend, SessionConfig{}))
	_, err := h.Handle(context.Background(), StageInput{RunID: "r", Spec: "x", Attempt: 1})
	if !errors.Is(err, ErrBadClassification) {
		t.Fatalf("err = %v, want ErrBadClassification", err)
	}
}
