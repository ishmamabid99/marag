package worker

import (
	"strings"
	"testing"
)

func TestFormatDirectory(t *testing.T) {
	got := FormatDirectory([]RepoInfo{
		{ID: "payments", Name: "payments-svc", Excerpt: "owns billing + currency"},
		{ID: "web", Name: "web-app", Excerpt: "next.js storefront"},
	})
	for _, want := range []string{"payments", "owns billing", "web", "next.js storefront"} {
		if !strings.Contains(got, want) {
			t.Fatalf("directory block missing %q:\n%s", want, got)
		}
	}
}

func TestFormatDirectoryEmpty(t *testing.T) {
	if got := FormatDirectory(nil); !strings.Contains(got, "no repositories") {
		t.Fatalf("empty directory should say so, got %q", got)
	}
}

func TestFormatDirectoryNoCard(t *testing.T) {
	got := FormatDirectory([]RepoInfo{{ID: "x", Name: "x-svc", Excerpt: ""}})
	if !strings.Contains(got, "no context doc") {
		t.Fatalf("repo without a card should be labeled, got %q", got)
	}
}
