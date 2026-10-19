// Command mcp-admin provisions and manages MCP access tokens. Usage:
//
//	mcp-admin create [-ttl <duration>] <name>   # mint a token (‑ttl e.g. 720h; omit = never expires)
//	mcp-admin list                              # list principals (no token material)
//	mcp-admin revoke <name>                     # delete a principal
//
// create generates a random token, stores only its SHA-256 hash in
// mcp_principals, and prints the plaintext token ONCE — hand it to the user;
// it cannot be recovered later.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/fin/multi-agent-rag/internal/config"
	"github.com/fin/multi-agent-rag/internal/mcpauth"
	"github.com/jackc/pgx/v5/pgxpool"
)

func usage() {
	log.Fatalf("usage:\n  mcp-admin create [-ttl <duration>] <name>\n  mcp-admin list\n  mcp-admin revoke <name>")
}

func main() {
	if len(os.Args) < 2 {
		usage()
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
	store, err := mcpauth.NewPGStore(ctx, pool)
	if err != nil {
		log.Fatalf("init auth store: %v", err)
	}

	switch os.Args[1] {
	case "create":
		doCreate(ctx, store, os.Args[2:])
	case "list":
		doList(ctx, store)
	case "revoke":
		if len(os.Args) != 3 {
			usage()
		}
		doRevoke(ctx, store, os.Args[2])
	default:
		usage()
	}
}

func doCreate(ctx context.Context, store *mcpauth.PGStore, args []string) {
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	ttl := fs.Duration("ttl", 0, "token lifetime, e.g. 720h (0 = never expires)")
	scope := fs.String("scope", mcpauth.ScopeFull, "token scope: full (trigger + knowledge base) | context (read-only knowledge base)")
	_ = fs.Parse(args)
	name := fs.Arg(0)
	if name == "" {
		log.Fatalf("usage: mcp-admin create [-ttl <duration>] [-scope full|context] <name>")
	}
	if !mcpauth.ValidScope(*scope) {
		log.Fatalf("invalid -scope %q (want %q or %q)", *scope, mcpauth.ScopeFull, mcpauth.ScopeContext)
	}

	var exp *time.Time
	if *ttl > 0 {
		t := time.Now().Add(*ttl)
		exp = &t
	}
	token, err := mcpauth.GenerateToken()
	if err != nil {
		log.Fatalf("generate token: %v", err)
	}
	if err := store.CreatePrincipal(ctx, name, token, *scope, exp); err != nil {
		log.Fatalf("create principal: %v", err)
	}
	expiry := "never"
	if exp != nil {
		expiry = exp.Format(time.RFC3339)
	}
	fmt.Printf("created principal %q (scope: %s, expires: %s)\nMARAG_MCP_TOKEN=%s\n(store this now; only its hash is kept)\n", name, *scope, expiry, token)
}

func doList(ctx context.Context, store *mcpauth.PGStore) {
	infos, err := store.List(ctx)
	if err != nil {
		log.Fatalf("list: %v", err)
	}
	if len(infos) == 0 {
		fmt.Println("no principals")
		return
	}
	fmt.Printf("%-24s %-8s %-20s %-20s %s\n", "NAME", "SCOPE", "CREATED", "EXPIRES", "STATUS")
	now := time.Now()
	for _, pi := range infos {
		expires, status := "never", "active"
		if pi.ExpiresAt != nil {
			expires = pi.ExpiresAt.Format(time.RFC3339)
			if now.After(*pi.ExpiresAt) {
				status = "expired"
			}
		}
		scope := pi.Scope
		if scope == "" {
			scope = mcpauth.ScopeFull
		}
		fmt.Printf("%-24s %-8s %-20s %-20s %s\n", pi.Name, scope, pi.CreatedAt.Format(time.RFC3339), expires, status)
	}
}

func doRevoke(ctx context.Context, store *mcpauth.PGStore, name string) {
	n, err := store.Revoke(ctx, name)
	if err != nil {
		log.Fatalf("revoke: %v", err)
	}
	if n == 0 {
		log.Fatalf("no such principal: %s", name)
	}
	fmt.Printf("revoked %q (%d token(s) removed)\n", name, n)
}
