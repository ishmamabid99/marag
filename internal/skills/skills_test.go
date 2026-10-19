package skills

import (
	"strings"
	"testing"
)

func TestAugmentIncludesBaseAndRole(t *testing.T) {
	out := Augment("SYSTEM-PROMPT", "developer")
	for _, want := range []string{"SYSTEM-PROMPT", "output contract", "Test-driven"} {
		if !strings.Contains(out, want) {
			t.Fatalf("Augment(developer) missing %q\n---\n%s", want, out)
		}
	}
}

func TestAugmentUnknownRoleKeepsBase(t *testing.T) {
	out := Augment("P", "no-such-role")
	if !strings.Contains(out, "P") || !strings.Contains(out, "output contract") {
		t.Fatalf("unknown role should still carry prompt+base: %s", out)
	}
}

func TestAugmentQAHasSpecCompliance(t *testing.T) {
	if !strings.Contains(Augment("", "qa"), "spec-compliance") {
		t.Fatal("qa skill should mention spec-compliance")
	}
}

// The hardening pass added explicit self-verification gates. Lock them in so a
// future edit can't quietly drop them.
func TestSelfVerificationGatesPresent(t *testing.T) {
	cases := []struct{ role, marker string }{
		{"developer", "go vet"}, // definition-of-done gate must require vet
		{"qa", "verdict gate"},  // per-criterion verdict gate
		{"risk-analyzer", "graph query"},
	}
	for _, c := range cases {
		if !strings.Contains(Augment("", c.role), c.marker) {
			t.Fatalf("%s skill missing hardening marker %q", c.role, c.marker)
		}
	}
	if !strings.Contains(Base(), "Self-verification gate") {
		t.Fatal("base skill missing the self-verification gate")
	}
}

// Repo-context grounding directives — the docs-first cross-repo grounding pass
// added these; lock them so an edit can't silently drop them.
func TestSkillsIncludeContextDirectives(t *testing.T) {
	cases := map[string]string{
		"requirement-analyzer": "proposed_repos",
		"system-architect":     "context card",
		"system-designer":      "conventions",
		"developer":            "read neighboring",
	}
	for role, want := range cases {
		if !strings.Contains(strings.ToLower(For(role)), strings.ToLower(want)) {
			t.Errorf("skill for %q missing directive %q", role, want)
		}
	}
	if !strings.Contains(strings.ToLower(Base()), "context card") {
		t.Error("base skill should tell agents to consult the repo context card")
	}
}
