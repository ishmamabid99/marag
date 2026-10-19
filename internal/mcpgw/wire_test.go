package mcpgw

import (
	"testing"

	"github.com/fin/multi-agent-rag/internal/orchestrator"
)

func TestToRunView(t *testing.T) {
	reason := "boom"
	r := orchestrator.Run{
		ID: "run1", Status: "failed", CurrentStage: "developing",
		Complexity: "moderate", Spec: "do the thing",
		RepoIDs: []string{"a", "b"}, QAIterations: 2, FailureReason: &reason,
	}
	v := toRunView(r)
	if v.RunID != "run1" || v.Status != "failed" || v.CurrentStage != "developing" {
		t.Fatalf("core fields wrong: %+v", v)
	}
	if v.Complexity != "moderate" || v.Spec != "do the thing" || v.QAIterations != 2 {
		t.Fatalf("meta fields wrong: %+v", v)
	}
	if len(v.RepoIDs) != 2 || v.FailureReason != "boom" {
		t.Fatalf("slice/ptr fields wrong: %+v", v)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("abcdef", 3); got != "abc…" {
		t.Fatalf("truncate long = %q", got)
	}
	if got := truncate("ab", 5); got != "ab" {
		t.Fatalf("truncate short = %q", got)
	}
}
