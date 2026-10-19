package ingestion

import (
	"path"
	"strings"
)

// ChunkKind distinguishes human-written context docs from source code, so
// retrieval can prefer docs when answering "what is this repo about".
type ChunkKind string

const (
	KindCode ChunkKind = "code"
	KindDoc  ChunkKind = "doc"
)

// docExactRank lists recognized context-doc basenames (case-insensitive) in
// priority order (index = rank; lower is higher priority).
var docExactRank = []string{"claude.md", "agents.md", "readme.md", "readme"}

// KindForPath classifies a repo-relative path as a context doc or source code.
func KindForPath(rel string) ChunkKind {
	if DocRank(rel) >= 0 {
		return KindDoc
	}
	return KindCode
}

// noiseExact lists machine-generated dependency lock / manifest basenames
// (case-insensitive). They are pinned hashes and version graphs, never
// human-authored context, and they pollute retrieval (a "book model" query
// should never surface go.sum). Excluded from the index entirely.
var noiseExact = map[string]bool{
	"go.sum":              true,
	"package-lock.json":   true,
	"npm-shrinkwrap.json": true,
	"yarn.lock":           true,
	"pnpm-lock.yaml":      true,
	"bun.lockb":           true,
	"packages.lock.json":  true,
	"cargo.lock":          true,
	"poetry.lock":         true,
	"pipfile.lock":        true,
	"composer.lock":       true,
	"gemfile.lock":        true,
	"gradle.lockfile":     true,
	"mix.lock":            true,
	"pubspec.lock":        true,
	"flake.lock":          true,
}

// IsNoiseFile reports whether a repo-relative path is machine-generated noise
// that must be kept out of the retrieval index: dependency lock files, minified
// bundles, and source maps. Applied centrally in ingestion so every entry point
// (the CLI walk, future Kafka-triggered ingest, tests) excludes it consistently.
// go.mod / package.json and other human-edited manifests are intentionally kept.
func IsNoiseFile(rel string) bool {
	base := strings.ToLower(path.Base(rel))
	if noiseExact[base] {
		return true
	}
	return strings.HasSuffix(base, ".min.js") ||
		strings.HasSuffix(base, ".min.css") ||
		strings.HasSuffix(base, ".map") || // source maps (*.js.map, *.css.map)
		strings.HasSuffix(base, ".lock") // generic lockfiles (Cargo.lock, etc.)
}

// secretExact lists local credential files that hold live API keys / tokens and
// must NEVER enter the index — a leak here is retrievable via MCP search_code.
// (A real incident: a working-tree ingest once swept a gitignored
// config.local.json with a live Anthropic key + git token into pgvector.)
var secretExact = map[string]bool{
	"config.local.json": true,
	".env":              true,
	"secrets.json":      true,
	"secrets.yaml":      true,
	"secrets.yml":       true,
	"credentials.json":  true,
	"id_rsa":            true,
	"id_ed25519":        true,
}

// IsSecretFile reports whether a repo-relative path is a local credentials file
// that must be excluded from the index regardless of ingest source. Committed
// *.example / *.sample / *.template placeholders are intentionally NOT matched —
// they document the shape, not real secrets.
func IsSecretFile(rel string) bool {
	base := strings.ToLower(path.Base(rel))
	if secretExact[base] {
		return true
	}
	isExample := strings.Contains(base, "example") || strings.Contains(base, "sample") || strings.Contains(base, "template")
	switch {
	case strings.HasPrefix(base, ".env.") && !isExample: // .env.local, .env.production
		return true
	case (strings.HasSuffix(base, ".local.json") || strings.HasSuffix(base, ".local.yaml") ||
		strings.HasSuffix(base, ".local.yml")) && !isExample: // *.local.* overrides
		return true
	case strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") ||
		strings.HasSuffix(base, ".p12") || strings.HasSuffix(base, ".pfx"): // private keys / cert bundles
		return true
	}
	return false
}

// ExcludeFromIndex is the single predicate ingestion call sites use to drop a
// file: machine-generated noise (retrieval clutter) or a local secrets file
// (security). Keep both concerns behind one call so every entry point agrees.
func ExcludeFromIndex(rel string) bool {
	return IsNoiseFile(rel) || IsSecretFile(rel)
}

// DocRank returns a priority rank for context docs (0 = highest) or -1 if the
// path is not a recognized context doc. Only Markdown (and READMEs) count.
func DocRank(rel string) int {
	base := strings.ToLower(path.Base(rel))
	for i, name := range docExactRank {
		if base == name {
			return i
		}
	}
	if strings.HasSuffix(base, ".md") {
		// Docs under a docs/ tree rank above stray top-level markdown; within
		// docs/, architecture/design/adr files rank first.
		lower := strings.ToLower(rel)
		inDocs := strings.Contains(lower, "docs/")
		archy := strings.Contains(base, "architect") || strings.Contains(base, "adr") || strings.Contains(base, "design")
		switch {
		case inDocs && archy:
			return len(docExactRank) + 0
		case inDocs:
			return len(docExactRank) + 1
		case archy:
			return len(docExactRank) + 2
		default:
			return len(docExactRank) + 3
		}
	}
	return -1
}
