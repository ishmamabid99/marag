package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/fin/multi-agent-rag/pkg/llm"
)

func qaSM(t *testing.T, reply string) *SessionManager {
	t.Helper()
	be := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: reply}, nil
	})
	return NewSessionManager(be, SessionConfig{})
}

func TestQAPasses(t *testing.T) {
	h := NewQA(qaSM(t, `{"qa_passed":true,"issues":[],"rationale":"matches request"}`))
	if h.Role() != "qa" {
		t.Fatalf("role = %q", h.Role())
	}
	res, err := h.Handle(context.Background(), StageInput{Stage: "qa", Spec: "add Add",
		PriorArtifacts: map[string][]byte{"developing": []byte(`{"summary":"added Add","tests_passed":true,"files_changed":["calc.go"]}`)}})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var art QAArtifact
	if err := json.Unmarshal(res.ArtifactJSON, &art); err != nil {
		t.Fatalf("artifact: %v", err)
	}
	if !art.QAPassed {
		t.Fatalf("art = %+v", art)
	}
}

func TestQAFailsAndPromotesFalse(t *testing.T) {
	h := NewQA(qaSM(t, `{"qa_passed":false,"issues":["missing subtraction"],"rationale":"incomplete"}`))
	res, err := h.Handle(context.Background(), StageInput{Stage: "qa", Spec: "add Add and Sub",
		PriorArtifacts: map[string][]byte{"developing": []byte(`{"summary":"added Add","tests_passed":true}`)}})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var art QAArtifact
	_ = json.Unmarshal(res.ArtifactJSON, &art)
	if art.QAPassed {
		t.Fatalf("expected qa_passed=false")
	}
}

func TestQAReviewsAllRepos(t *testing.T) {
	var gotUser string
	be := llm.NewFake(func(_ context.Context, req llm.Request) (llm.Response, error) {
		if len(req.Messages) > 0 {
			gotUser = req.Messages[len(req.Messages)-1].Content
		}
		return llm.Response{Text: `{"qa_passed":true,"issues":[],"rationale":"ok"}`}, nil
	})
	sm := NewSessionManager(be, SessionConfig{})
	dev := `{"summary":"multi","repos":[
		{"repo_id":"payments","tests_passed":true,"files_changed":["convert.go"]},
		{"repo_id":"checkout","tests_passed":true,"files_changed":["cart.go"]}]}`
	res, err := NewQA(sm).Handle(context.Background(), StageInput{Stage: "qa", Spec: "multi-currency",
		PriorArtifacts: map[string][]byte{"developing": []byte(dev)}})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var art QAArtifact
	if err := json.Unmarshal(res.ArtifactJSON, &art); err != nil {
		t.Fatalf("artifact: %v", err)
	}
	if !art.QAPassed {
		t.Fatalf("art = %+v", art)
	}
	// The review prompt must reference BOTH repos.
	if !strings.Contains(gotUser, "payments") || !strings.Contains(gotUser, "checkout") {
		t.Fatalf("qa prompt did not cover all repos:\n%s", gotUser)
	}
}

func TestQAErrorsWithoutDeveloperArtifact(t *testing.T) {
	h := NewQA(qaSM(t, `{"qa_passed":true}`))
	_, err := h.Handle(context.Background(), StageInput{Stage: "qa", Spec: "x"})
	if err == nil {
		t.Fatalf("expected error when no developer artifact present")
	}
}
