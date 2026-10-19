package ingestion

import "testing"

func TestKindForPath(t *testing.T) {
	cases := map[string]ChunkKind{
		"CLAUDE.md":               KindDoc,
		"README.md":               KindDoc,
		"docs/architecture.md":    KindDoc,
		"AGENTS.md":               KindDoc,
		"internal/worker/pool.go": KindCode,
		"main.py":                 KindCode,
		"notes.txt":               KindCode, // .txt is not a recognized context doc
	}
	for path, want := range cases {
		if got := KindForPath(path); got != want {
			t.Errorf("KindForPath(%q)=%q want %q", path, got, want)
		}
	}
}

func TestIsNoiseFile(t *testing.T) {
	noise := []string{
		"go.sum", "GO.SUM", "pkg/go.sum",
		"package-lock.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb",
		"Cargo.lock", "poetry.lock", "Pipfile.lock", "composer.lock",
		"Gemfile.lock", "gradle.lockfile", "mix.lock", "flake.lock",
		"web/app.min.js", "static/site.min.css", "dist/bundle.js.map",
		"some/thing.lock",
	}
	for _, p := range noise {
		if !IsNoiseFile(p) {
			t.Errorf("IsNoiseFile(%q) = false, want true (machine-generated noise)", p)
		}
	}
	keep := []string{
		"go.mod", "package.json", "main.go", "README.md", "CLAUDE.md",
		"pkg/models/book.go", "config.json", "Makefile", "app.js",
	}
	for _, p := range keep {
		if IsNoiseFile(p) {
			t.Errorf("IsNoiseFile(%q) = true, want false (human-authored / real code)", p)
		}
	}
}

func TestIsSecretFile(t *testing.T) {
	secret := []string{
		"config.local.json", "app/config.local.json", ".env", ".env.local",
		".env.production", "secrets.json", "secrets.yaml", "credentials.json",
		"deploy/tls.pem", "server.key", "cert.p12", "id_rsa", "id_ed25519",
		"settings.local.yaml",
	}
	for _, p := range secret {
		if !IsSecretFile(p) {
			t.Errorf("IsSecretFile(%q) = false, want true (local credentials file)", p)
		}
	}
	// Committed placeholders / templates and normal config must NOT be excluded.
	keep := []string{
		"config.json", "config.real-run.example.json", ".env.example",
		".env.sample", "config.template.json", "main.go", "README.md",
	}
	for _, p := range keep {
		if IsSecretFile(p) {
			t.Errorf("IsSecretFile(%q) = true, want false (template/placeholder/real code)", p)
		}
	}
}

func TestDocRankOrder(t *testing.T) {
	// CLAUDE.md outranks README outranks a generic doc; non-doc is -1.
	if !(DocRank("CLAUDE.md") < DocRank("README.md") &&
		DocRank("README.md") < DocRank("docs/guide.md")) {
		t.Fatalf("unexpected doc ranking: CLAUDE=%d README=%d guide=%d",
			DocRank("CLAUDE.md"), DocRank("README.md"), DocRank("docs/guide.md"))
	}
	if DocRank("main.go") != -1 {
		t.Fatalf("non-doc should rank -1, got %d", DocRank("main.go"))
	}
}
