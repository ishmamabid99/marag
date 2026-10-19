package ingestion

import (
	"fmt"
	"sort"
	"strings"
)

// ContextCardBudgetBytes bounds the raw doc text captured per repo so it can be
// cheaply injected into reasoning prompts. ~24KB ≈ 6k tokens.
const ContextCardBudgetBytes = 24 * 1024

// BuildContextCard concatenates a repo's highest-priority context docs (ranked
// by DocRank) into one budget-capped card, each section prefixed with a
// "===== <path> =====" header. Returns "" when there are no context docs.
func BuildContextCard(files map[string]string) string {
	type doc struct {
		path string
		rank int
	}
	var docs []doc
	for p := range files {
		if r := DocRank(p); r >= 0 {
			docs = append(docs, doc{p, r})
		}
	}
	if len(docs) == 0 {
		return ""
	}
	sort.Slice(docs, func(i, j int) bool {
		if docs[i].rank != docs[j].rank {
			return docs[i].rank < docs[j].rank
		}
		return docs[i].path < docs[j].path // stable, deterministic
	})

	var b strings.Builder
	for _, d := range docs {
		if b.Len() >= ContextCardBudgetBytes {
			break
		}
		header := fmt.Sprintf("===== %s =====\n", d.path)
		remaining := ContextCardBudgetBytes - b.Len()
		body := files[d.path]
		if len(header)+len(body) > remaining {
			// Take as much of this doc as fits (whole header + truncated body).
			take := remaining - len(header)
			if take < 0 {
				take = 0
			}
			if take > len(body) {
				take = len(body)
			}
			body = body[:take]
		}
		b.WriteString(header)
		b.WriteString(body)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}
