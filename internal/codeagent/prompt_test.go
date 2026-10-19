package codeagent

import (
	"strings"
	"testing"
)

func TestBuildPromptIncludesRepoContext(t *testing.T) {
	p := buildPrompt(Task{
		Spec:          "add multi-currency",
		TargetRepoID:  "checkout-web",
		PriorRepoWork: "repo payments-svc: added Converter (branch marag/run-1)",
		RepoIDs:       []string{"payments-svc", "checkout-web"},
	})
	if !strings.Contains(p, "checkout-web") {
		t.Fatalf("prompt missing target repo:\n%s", p)
	}
	if !strings.Contains(p, "payments-svc: added Converter") {
		t.Fatalf("prompt missing prior repo work:\n%s", p)
	}
}

func TestBuildPromptOmitsEmptyRepoContext(t *testing.T) {
	p := buildPrompt(Task{Spec: "x"})
	if strings.Contains(p, "already implemented") || strings.Contains(p, "You are implementing changes in repository") {
		t.Fatalf("prompt should omit empty repo context:\n%s", p)
	}
}
