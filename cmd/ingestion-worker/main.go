// Command ingestion-worker ingests a local repository snapshot into pgvector
// for a registered repo. Kafka-triggered ingestion is a later addition; this
// entrypoint drives one ingestion directly from a directory path.
package main

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/fin/multi-agent-rag/internal/config"
	"github.com/fin/multi-agent-rag/internal/ingestion"
	"github.com/fin/multi-agent-rag/internal/models"
	"github.com/fin/multi-agent-rag/internal/registry"
	"github.com/fin/multi-agent-rag/pkg/embedder"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx := context.Background()
	cfg := config.Get()

	dsn := cfg.Database.DSN()
	repoID := os.Getenv("INGEST_REPO_ID")
	dir := os.Getenv("INGEST_REPO_DIR")
	if dsn == "" || repoID == "" || dir == "" {
		log.Fatal("INGEST_DATABASE_URL, INGEST_REPO_ID, and INGEST_REPO_DIR are required")
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()

	store, err := registry.NewPGStore(ctx, pool)
	if err != nil {
		log.Fatalf("init store: %v", err)
	}
	svc := registry.NewService(store)

	// Embedder: Ollama if configured, else the deterministic fake.
	var emb embedder.Embedder = embedder.NewFake(models.DefaultEmbeddingDim)
	if base := cfg.Embeddings.OllamaURL; base != "" {
		model := cfg.Embeddings.OllamaModel
		if model == "" {
			model = "nomic-embed-text"
		}
		emb = embedder.NewOllama(base, model, models.DefaultEmbeddingDim)
	}

	files, err := readDir(dir)
	if err != nil {
		log.Fatalf("read dir: %v", err)
	}

	cstore := ingestion.NewContextStore(pool)
	if err := cstore.EnsureSchema(ctx); err != nil {
		log.Fatalf("ensure context schema: %v", err)
	}
	ing := ingestion.NewIngestor(pool, svc, ingestion.NewVectorWriter(pool), emb).
		WithContextStore(cstore)
	res, err := ing.Ingest(ctx, repoID, files, 0)
	if err != nil {
		log.Fatalf("ingest: %v", err)
	}
	log.Printf("ingested repo=%s version=%d chunks=%d oldDeleted=%d",
		repoID, res.Version, res.ChunksWritten, res.OldVersionDeleted)
}

// vendorDirs are non-hidden directories skipped during ingestion: they hold
// third-party/generated content that pollutes retrieval and can be huge.
var vendorDirs = map[string]bool{
	"node_modules": true, "venv": true, "vendor": true,
	"__pycache__": true, "dist": true, "build": true, "target": true,
}

// readDir walks dir and returns text files keyed by path relative to dir. It
// tolerates real-repo hygiene: hidden + vendor dirs are skipped, symlinks are
// not followed (may be broken or escape the tree), and any unreadable entry is
// skipped rather than aborting the whole ingestion.
func readDir(dir string) (map[string]string, error) {
	files := make(map[string]string)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries (e.g. broken symlinks) — don't abort
		}
		if d.IsDir() {
			name := d.Name()
			if name != "." && (strings.HasPrefix(name, ".") || vendorDirs[name]) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // don't follow symlinks
		}
		if ingestion.ExcludeFromIndex(d.Name()) {
			return nil // noise (lock/minified) or a local secrets file — don't even read it
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Size() > 1<<20 { // skip files > 1 MiB
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil // unreadable file — skip
		}
		if !isText(content) {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		files[rel] = string(content)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", dir, err)
	}
	return files, nil
}

// isText reports whether content looks like UTF-8 text (no NUL bytes).
func isText(content []byte) bool {
	for _, b := range content {
		if b == 0 {
			return false
		}
	}
	return true
}
