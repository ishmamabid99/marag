package ingestion

import (
	"strings"
	"testing"
)

func TestBuildContextCardPrefersClaudeThenReadme(t *testing.T) {
	files := map[string]string{
		"main.go":   "package main",
		"README.md": "# My Service\nDoes X.",
		"CLAUDE.md": "# Guidance\nThis repo owns billing.",
	}
	card := BuildContextCard(files)
	if card == "" {
		t.Fatal("expected a card")
	}
	// CLAUDE.md content appears before README content.
	ci := strings.Index(card, "owns billing")
	ri := strings.Index(card, "Does X")
	if ci < 0 || ri < 0 || ci > ri {
		t.Fatalf("expected CLAUDE.md before README in card:\n%s", card)
	}
	// Path headers are present; code files are excluded.
	if !strings.Contains(card, "CLAUDE.md") || strings.Contains(card, "package main") {
		t.Fatalf("card should header docs and exclude code:\n%s", card)
	}
}

func TestBuildContextCardEmptyWhenNoDocs(t *testing.T) {
	if got := BuildContextCard(map[string]string{"main.go": "package main"}); got != "" {
		t.Fatalf("expected empty card, got %q", got)
	}
}

func TestBuildContextCardBudget(t *testing.T) {
	big := strings.Repeat("x", ContextCardBudgetBytes*2)
	card := BuildContextCard(map[string]string{"CLAUDE.md": big})
	if len(card) > ContextCardBudgetBytes+256 { // +slack for the path header
		t.Fatalf("card exceeded budget: %d", len(card))
	}
}
