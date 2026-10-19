// Command mcp-gateway serves the "fin-context" MCP server: developers add it to
// Claude Code/Desktop to trigger runs (submit / status / approve / reject) and
// to search cross-repo context (list_repos / search_code), all grounded in the
// platform's real orchestrator, registry, and retrieval state.
//
// Two transports:
//   - stdio (default): stdout carries the JSON-RPC channel, so all logging goes
//     to stderr. Auth is the single MARAG_MCP_TOKEN env var, pinned at launch.
//   - HTTP (-http :PORT): a long-running Streamable-HTTP server for a whole
//     team; auth is the per-request Authorization: Bearer token. Deployed behind
//     mcp.marag.fin.com.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fin/multi-agent-rag/internal/config"
	"github.com/fin/multi-agent-rag/internal/kafka"
	"github.com/fin/multi-agent-rag/internal/mcpauth"
	"github.com/fin/multi-agent-rag/internal/mcpgw"
	"github.com/fin/multi-agent-rag/internal/mcpserver"
	"github.com/jackc/pgx/v5/pgxpool"
)

// shutdownTimeout bounds graceful HTTP shutdown.
const shutdownTimeout = 10 * time.Second

func main() {
	// stdout is the MCP channel in stdio mode; every log line goes to stderr.
	log.SetOutput(os.Stderr)

	httpAddr := flag.String("http", "", "serve MCP over HTTP on this address (e.g. :8090); empty = stdio")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := config.Get()

	dsn := cfg.Database.DSN()
	brokers := cfg.Kafka.Brokers
	if dsn == "" || len(brokers) == 0 {
		log.Fatal("database.url and kafka.brokers are required")
	}
	kafkaAuth := kafka.ClientAuth{
		SASLUser:    cfg.Kafka.Username,
		SASLPass:    cfg.Kafka.Password,
		TLSEnabled:  cfg.Kafka.TLSEnabled,
		Compression: cfg.Kafka.ProducerCompression,
		MaxRetries:  cfg.Kafka.ProducerMaxRetries,
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()

	prod, err := kafka.NewProducer(brokers, kafka.WithAuth(kafkaAuth))
	if err != nil {
		log.Fatalf("init producer: %v", err)
	}
	defer prod.Close()

	g, err := mcpgw.BuildGateway(ctx, cfg, pool, prod)
	if err != nil {
		log.Fatalf("build gateway: %v", err)
	}

	s := mcpserver.New("fin-context", "0.1.0")
	mcpgw.RegisterTools(s, g)
	s.SetToolCallGate(mcpgw.ScopeGate())

	authStore, err := mcpauth.NewPGStore(ctx, pool)
	if err != nil {
		log.Fatalf("init auth store: %v", err)
	}

	if *httpAddr != "" {
		serveHTTP(ctx, *httpAddr, newHTTPMux(s, authStore), brokers)
		return
	}
	serveStdio(ctx, s, authStore, brokers)
}

// newHTTPMux builds the HTTP routes: /mcp (bearer-authed MCP transport) and a
// trivial /healthz for edge/health checks.
func newHTTPMux(s *mcpserver.Server, authStore mcpauth.TokenStore) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpgw.BearerAuth(authStore, s.HTTPHandler()))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

// serveHTTP runs the HTTP transport until ctx is cancelled, then drains.
func serveHTTP(ctx context.Context, addr string, h http.Handler, brokers []string) {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("mcp-gateway: listen: %v", err)
		}
	}()
	log.Printf("mcp-gateway: serving MCP over HTTP on %s (path /mcp; brokers=%v)", addr, brokers)

	<-ctx.Done()
	log.Print("mcp-gateway: shutting down")
	shCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shCtx); err != nil {
		log.Printf("mcp-gateway: shutdown: %v", err)
	}
}

// serveStdio runs the stdio transport: authenticate the single launch token
// once and pin its scope on the context. A missing/invalid token → empty scope
// → all tool calls denied by ScopeGate (initialize/tools/list still respond).
func serveStdio(ctx context.Context, s *mcpserver.Server, authStore mcpauth.TokenStore, brokers []string) {
	scope := ""
	if principal, err := authStore.Authenticate(ctx, os.Getenv("MARAG_MCP_TOKEN")); err != nil {
		log.Printf("mcp-gateway: UNAUTHENTICATED (%v) — tool calls denied; set MARAG_MCP_TOKEN", err)
	} else {
		log.Printf("mcp-gateway: authenticated as %q (scope %q)", principal.Name, principal.Scope)
		scope = principal.Scope
	}
	ctx = mcpgw.WithScope(ctx, scope)

	log.Printf("mcp-gateway: serving fin-context over stdio (brokers=%v)", brokers)
	if err := s.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
