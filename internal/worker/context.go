package worker

import (
	"context"
	"fmt"
	"strings"

	"github.com/fin/multi-agent-rag/internal/ingestion"
)

// maxInjectedContextBytes bounds the total context-card text injected into a
// single reasoning prompt (multi-repo runs concatenate several cards).
const maxInjectedContextBytes = 2 * ingestion.ContextCardBudgetBytes

// appendRepoContext appends each in-scope repo's context card to a reasoning
// prompt, bounded by maxInjectedContextBytes. Returns user unchanged if dir is
// nil or no cards are available.
func appendRepoContext(ctx context.Context, dir RepoDirectory, user string, repoIDs []string) string {
	if dir == nil {
		return user
	}
	var b strings.Builder
	for _, id := range repoIDs {
		if b.Len() >= maxInjectedContextBytes {
			break
		}
		card, err := dir.Card(ctx, id)
		if err != nil || card == "" {
			continue
		}
		if b.Len()+len(card) > maxInjectedContextBytes {
			card = card[:maxInjectedContextBytes-b.Len()]
		}
		fmt.Fprintf(&b, "\n\n===== context: %s =====\n%s", id, card)
	}
	if b.Len() == 0 {
		return user
	}
	return user + "\n\nExisting repository context (honor these conventions and architecture):" + b.String()
}

// RepoInfo is a repo the analyzer can consider, with a bounded excerpt of its
// context card so the model learns what each repo is about.
type RepoInfo struct {
	ID      string
	Name    string
	Excerpt string // leading bytes of the repo's active context card
}

// RepoDirectory lists candidate repos with context excerpts and loads the full
// context card for one repo. Implemented in cmd/worker over the registry +
// ingestion.ContextStore.
type RepoDirectory interface {
	List(ctx context.Context, excerptBytes int) ([]RepoInfo, error)
	Card(ctx context.Context, repoID string) (string, error)
}

// FormatDirectory renders a compact, model-readable catalog of registered
// repos and what each is about, for grounding repo selection.
func FormatDirectory(repos []RepoInfo) string {
	if len(repos) == 0 {
		return "(no repositories are registered)"
	}
	var b strings.Builder
	b.WriteString("Registered repositories (id — name — what it is):\n")
	for _, r := range repos {
		ex := strings.ReplaceAll(strings.TrimSpace(r.Excerpt), "\n", " ")
		if ex == "" {
			ex = "(no context doc ingested)"
		}
		fmt.Fprintf(&b, "- %s — %s — %s\n", r.ID, r.Name, ex)
	}
	return strings.TrimSpace(b.String())
}
