package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fin/multi-agent-rag/pkg/llm"
)

func TestArchitectInjectsRepoContext(t *testing.T) {
	dir := fakeDir{cards: map[string]string{"payments": "owns billing and currency"}}
	reply := `{"decisions":[{"decision":"d","rationale":"r","scope":"s"}],"components":[],"new_interfaces":[],"integration_points":[],"migration_strategy":""}`
	be := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: reply}, nil
	})
	h := NewSystemArchitect(NewSessionManager(be, SessionConfig{})).WithRepoDirectory(dir)
	if _, err := h.Handle(context.Background(), StageInput{Stage: "architecting", Spec: "x", RepoIDs: []string{"payments"}}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := be.Calls()[0].Messages[0].Content
	if !strings.Contains(got, "owns billing and currency") {
		t.Fatalf("architect prompt missing repo context card:\n%s", got)
	}
}

func TestSystemArchitectProducesArtifact(t *testing.T) {
	reply := `{"decisions":[{"decision":"introduce currency service","rationale":"isolate FX","scope":"payments"}],
	 "components":[{"component":"CurrencyConverter","repo_id":"payments","change_type":"create","description":"new pkg"}],
	 "new_interfaces":["Converter.Convert(amount,from,to)"],
	 "integration_points":["checkout calls Converter"],
	 "migration_strategy":"backfill existing orders lazily"}`
	be := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: reply}, nil
	})
	sm := NewSessionManager(be, SessionConfig{})
	h := NewSystemArchitect(sm)
	if h.Role() != "system-architect" {
		t.Fatalf("role = %q", h.Role())
	}
	res, err := h.Handle(context.Background(), StageInput{Stage: "architecting", Spec: "multi-currency", RepoIDs: []string{"payments"}})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var art ArchitectureArtifact
	if err := json.Unmarshal(res.ArtifactJSON, &art); err != nil {
		t.Fatalf("artifact: %v", err)
	}
	if len(art.Decisions) != 1 || art.Decisions[0].Scope != "payments" || len(art.Components) != 1 {
		t.Fatalf("artifact = %+v", art)
	}
	calls := be.Calls()
	if calls[0].Model != "claude-opus-4-8" || !calls[0].Thinking {
		t.Fatalf("request = %+v", calls[0])
	}
}

func TestSystemArchitectRejectsEmptyDecisions(t *testing.T) {
	be := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: `{"decisions":[]}`}, nil
	})
	sm := NewSessionManager(be, SessionConfig{})
	_, err := NewSystemArchitect(sm).Handle(context.Background(), StageInput{Stage: "architecting", Spec: "x"})
	if !errors.Is(err, ErrBadArtifact) {
		t.Fatalf("err = %v, want ErrBadArtifact", err)
	}
}
