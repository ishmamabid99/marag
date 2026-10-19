package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fin/multi-agent-rag/pkg/llm"
)

func TestDesignerInjectsRepoContext(t *testing.T) {
	dir := fakeDir{cards: map[string]string{"payments": "double-entry ledger conventions"}}
	reply := `{"file_changes":[{"file_path":"a.go","repo_id":"payments","change_type":"create","description":"d","complexity_hint":"standard"}],"test_cases":[],"implementation_order":"o","edge_cases":[]}`
	be := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: reply}, nil
	})
	h := NewSystemDesigner(NewSessionManager(be, SessionConfig{})).WithRepoDirectory(dir)
	if _, err := h.Handle(context.Background(), StageInput{Stage: "designing", Spec: "x", RepoIDs: []string{"payments"}}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	got := be.Calls()[0].Messages[0].Content
	if !strings.Contains(got, "double-entry ledger conventions") {
		t.Fatalf("designer prompt missing repo context card:\n%s", got)
	}
}

func TestSystemDesignerProducesArtifact(t *testing.T) {
	reply := `{"file_changes":[{"file_path":"payments/convert.go","repo_id":"payments","change_type":"create","description":"converter","complexity_hint":"standard"}],
	 "test_cases":[{"name":"TestConvert","description":"converts USD->EUR","file_path":"payments/convert_test.go","type":"unit"}],
	 "implementation_order":"converter, then wire into checkout",
	 "edge_cases":["zero amount","unknown currency"]}`
	be := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: reply}, nil
	})
	sm := NewSessionManager(be, SessionConfig{})
	h := NewSystemDesigner(sm)
	if h.Role() != "system-designer" {
		t.Fatalf("role = %q", h.Role())
	}
	res, err := h.Handle(context.Background(), StageInput{Stage: "designing", Spec: "multi-currency", RepoIDs: []string{"payments"}})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var art DesignArtifact
	if err := json.Unmarshal(res.ArtifactJSON, &art); err != nil {
		t.Fatalf("artifact: %v", err)
	}
	if len(art.FileChanges) != 1 || art.FileChanges[0].ComplexityHint != "standard" || len(art.TestCases) != 1 {
		t.Fatalf("artifact = %+v", art)
	}
	calls := be.Calls()
	if calls[0].Model != "claude-opus-4-8" || !calls[0].Thinking {
		t.Fatalf("request = %+v", calls[0])
	}
}

func TestSystemDesignerRejectsNoFileChanges(t *testing.T) {
	be := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: `{"file_changes":[]}`}, nil
	})
	sm := NewSessionManager(be, SessionConfig{})
	_, err := NewSystemDesigner(sm).Handle(context.Background(), StageInput{Stage: "designing", Spec: "x"})
	if !errors.Is(err, ErrBadArtifact) {
		t.Fatalf("err = %v, want ErrBadArtifact", err)
	}
}
