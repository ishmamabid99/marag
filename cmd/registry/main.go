// Command registry runs the repo registry gRPC service.
package main

import (
	"context"
	"log"
	"net"

	"github.com/fin/multi-agent-rag/internal/config"
	"github.com/fin/multi-agent-rag/internal/registry"
	"github.com/fin/multi-agent-rag/internal/registry/registrypb"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
)

func main() {
	ctx := context.Background()

	cfg := config.Get()

	dsn := cfg.Database.DSN()
	if dsn == "" {
		log.Fatal("REGISTRY_DATABASE_URL is required")
	}
	addr := cfg.Registry.GRPCAddr
	if addr == "" {
		addr = ":8081"
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

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v", addr, err)
	}
	srv := grpc.NewServer()
	registrypb.RegisterRegistryServiceServer(srv, registry.NewGRPCServer(svc))

	log.Printf("registry listening on %s", addr)
	if err := srv.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
