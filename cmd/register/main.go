// Command register adds a repository to the registry so it can be ingested and
// targeted by runs. There is otherwise only the gRPC path; this is the CLI.
//
//	register -id marag -name multi-agent-rag -provider github \
//	         -url https://github.com/fin/multi-agent-rag -branch main
package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/fin/multi-agent-rag/internal/config"
	"github.com/fin/multi-agent-rag/internal/models"
	"github.com/fin/multi-agent-rag/internal/registry"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	id := flag.String("id", "", "repo id (required)")
	name := flag.String("name", "", "repo display name (required)")
	provider := flag.String("provider", "github", "github | gitlab")
	url := flag.String("url", "", "clonable remote URL (required)")
	branch := flag.String("branch", "main", "default branch")
	embModel := flag.String("embed-model", "nomic-embed-text", "embedding model name")
	flag.Parse()
	if *id == "" || *name == "" || *url == "" {
		log.Fatal("usage: register -id <id> -name <name> -url <remote-url> [-provider github] [-branch main]")
	}

	cfg := config.Get()
	if cfg.Database.DSN() == "" {
		log.Fatal("database.url is required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.Database.DSN())
	if err != nil {
		log.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()
	store, err := registry.NewPGStore(ctx, pool)
	if err != nil {
		log.Fatalf("init registry store: %v", err)
	}
	svc := registry.NewService(store)

	out, err := svc.Register(ctx, models.Repo{
		ID:             *id,
		Name:           *name,
		Provider:       models.GitProvider(*provider),
		RemoteURL:      *url,
		DefaultBranch:  *branch,
		EmbeddingModel: *embModel,
		EmbeddingDim:   models.DefaultEmbeddingDim,
	})
	if err != nil {
		log.Fatalf("register: %v", err)
	}
	fmt.Printf("registered repo id=%s name=%s provider=%s branch=%s\n", out.ID, out.Name, out.Provider, out.DefaultBranch)
}
