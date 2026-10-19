package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/fin/multi-agent-rag/pkg/llm"
)

func riskSM(t *testing.T, reply string) *SessionManager {
	t.Helper()
	be := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: reply}, nil
	})
	return NewSessionManager(be, SessionConfig{})
}

func TestRiskAnalyzerProducesArtifact(t *testing.T) {
	reply := `{"blast_radius":"high",
	 "affected_flows":[{"flow_name":"checkout","repo_id":"payments","entry_points":["POST /pay"],"impact_desc":"touches settlement"}],
	 "breaking_risks":["changes public API"],
	 "gate_recommended":true,
	 "gate_reason":"public API change"}`
	h := NewRiskAnalyzer(riskSM(t, reply))
	if h.Role() != "risk-analyzer" {
		t.Fatalf("role = %q", h.Role())
	}
	res, err := h.Handle(context.Background(), StageInput{RunID: "r1", Stage: "risk", Spec: "add multi-currency", RepoIDs: []string{"payments"}})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var art RiskArtifact
	if err := json.Unmarshal(res.ArtifactJSON, &art); err != nil {
		t.Fatalf("artifact: %v", err)
	}
	if art.BlastRadius != "high" || !art.GateRecommended || len(art.AffectedFlows) != 1 {
		t.Fatalf("artifact = %+v", art)
	}
	if art.AffectedFlows[0].FlowName != "checkout" {
		t.Fatalf("flow = %+v", art.AffectedFlows[0])
	}
}

func TestRiskAnalyzerRejectsBadBlastRadius(t *testing.T) {
	h := NewRiskAnalyzer(riskSM(t, `{"blast_radius":"apocalyptic"}`))
	_, err := h.Handle(context.Background(), StageInput{Stage: "risk", Spec: "x"})
	if !errors.Is(err, ErrBadArtifact) {
		t.Fatalf("err = %v, want ErrBadArtifact", err)
	}
}

func TestRiskAnalyzerUsesThinking(t *testing.T) {
	be := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: `{"blast_radius":"low"}`}, nil
	})
	sm := NewSessionManager(be, SessionConfig{})
	if _, err := NewRiskAnalyzer(sm).Handle(context.Background(), StageInput{Stage: "risk", Spec: "x"}); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	calls := be.Calls()
	if len(calls) != 1 || calls[0].Model != "claude-sonnet-5" || !calls[0].Thinking {
		t.Fatalf("request = %+v", calls)
	}
}
