// Command retrieval-gateway runs a one-off cross-repo retrieval query and
// prints the ranked results. MCP/gRPC transport is a later addition.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/fin/multi-agent-rag/internal/config"
	"github.com/fin/multi-agent-rag/internal/models"
	"github.com/fin/multi-agent-rag/internal/registry"
	"github.com/fin/multi-agent-rag/internal/retrieval"
	"github.com/fin/multi-agent-rag/pkg/embedder"
	"github.com/fin/multi-agent-rag/pkg/reranker"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx := context.Background()
	cfg := config.Get()

	dsn := cfg.Database.DSN()
	query := os.Getenv("RETRIEVAL_QUERY")
	repoCSV := os.Getenv("RETRIEVAL_REPO_IDS")
	if dsn == "" || query == "" || repoCSV == "" {
		log.Fatal("RETRIEVAL_DATABASE_URL, RETRIEVAL_QUERY, and RETRIEVAL_REPO_IDS are required")
	}
	var repoIDs []string
	for _, id := range strings.Split(repoCSV, ",") {
		if id = strings.TrimSpace(id); id != "" {
			repoIDs = append(repoIDs, id)
		}
	}
	if len(repoIDs) == 0 {
		log.Fatal("RETRIEVAL_REPO_IDS contained no valid repo IDs")
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

	var emb embedder.Embedder = embedder.NewFake(models.DefaultEmbeddingDim)
	if base := cfg.Embeddings.OllamaURL; base != "" {
		model := cfg.Embeddings.OllamaModel
		if model == "" {
			model = "nomic-embed-text"
		}
		emb = embedder.NewOllama(base, model, models.DefaultEmbeddingDim)
	}

	rr := reranker.New(cfg.Reranker.Kind, cfg.Reranker.URL, cfg.Reranker.Model)

	g := retrieval.NewGateway(pool, svc, emb, rr)
	resp, err := g.Search(ctx, retrieval.SearchRequest{Query: query, RepoIDs: repoIDs})
	if err != nil {
		log.Fatalf("search: %v", err)
	}

	for i, r := range resp.Results {
		fmt.Printf("%d. [%s] %s#%d (score=%.3f dist=%.4f)\n", i+1, r.RepoID, r.FilePath, r.ChunkIndex, r.Score, r.Distance)
	}
	for _, s := range resp.Staleness {
		if s.Stale {
			fmt.Printf("stale: %s (commits_behind=%d)\n", s.RepoID, s.CommitsBehind)
		}
	}
}
