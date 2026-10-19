// Command worker runs the agent worker pool: it consumes run.stage.requested,
// dispatches to role handlers, checkpoints, and emits stage.completed/failed.
package main

import (
	"context"
	"fmt"
	"log"
	"os/signal"
	"syscall"

	"github.com/fin/multi-agent-rag/internal/codeagent"
	"github.com/fin/multi-agent-rag/internal/config"
	"github.com/fin/multi-agent-rag/internal/ingestion"
	"github.com/fin/multi-agent-rag/internal/kafka"
	"github.com/fin/multi-agent-rag/internal/managedagent"
	"github.com/fin/multi-agent-rag/internal/models"
	"github.com/fin/multi-agent-rag/internal/registry"
	"github.com/fin/multi-agent-rag/internal/retrieval"
	"github.com/fin/multi-agent-rag/internal/sandbox"
	"github.com/fin/multi-agent-rag/internal/worker"
	"github.com/fin/multi-agent-rag/pkg/embedder"
	"github.com/fin/multi-agent-rag/pkg/llm"
	"github.com/fin/multi-agent-rag/pkg/reranker"
	"github.com/jackc/pgx/v5/pgxpool"
)

// maxSnippetLen bounds the retrieval snippet fed into the developer's tool
// loop so a single search_code result doesn't blow up the prompt (mirrors
// cmd/mcp-gateway's bound on the same retrieval content).
const maxSnippetLen = 400

// registryResolver is a registry-backed worker.RepoResolver: it looks up a
// repo's hosting metadata and builds an authenticated clone URL from the
// provider's env token.
type registryResolver struct {
	reg *registry.Service
}

func (r *registryResolver) Resolve(ctx context.Context, repoID string) (worker.RepoCheckout, error) {
	repo, err := r.reg.Get(ctx, repoID)
	if err != nil {
		return worker.RepoCheckout{}, fmt.Errorf("registry resolver: get %q: %w", repoID, err)
	}
	provider := string(repo.Provider)
	token := tokenFor(provider)
	return worker.RepoCheckout{
		CloneURL:   sandbox.GitUserURL(repo.RemoteURL, provider, token), // username-only, no secret
		Token:      token,                                               // supplied to git out-of-band (askpass)
		BaseBranch: repo.DefaultBranch,
	}, nil
}

// plainURLResolver is a registry-backed worker.RepoResolver that returns the
// registry's RemoteURL UNMODIFIED (no token injection): the Managed Agents
// developer backend authenticates git clone/push via a Vault credential, not
// a token embedded in the URL. Distinct from registryResolver, which is used
// by the local developer backend.
type plainURLResolver struct {
	reg *registry.Service
}

func (r *plainURLResolver) Resolve(ctx context.Context, repoID string) (worker.RepoCheckout, error) {
	repo, err := r.reg.Get(ctx, repoID)
	if err != nil {
		return worker.RepoCheckout{}, fmt.Errorf("plain-url resolver: get %q: %w", repoID, err)
	}
	return worker.RepoCheckout{CloneURL: repo.RemoteURL, BaseBranch: repo.DefaultBranch}, nil
}

// tokenFor reads the provider's API token from config. Never logged.
func tokenFor(provider string) string {
	cfg := config.Get()
	switch provider {
	case "github":
		return cfg.Git.GitHubToken
	case "gitlab":
		return cfg.Git.GitLabToken
	default:
		return ""
	}
}

// codeagentSearcher adapts *retrieval.Gateway to codeagent.ContextSearcher so
// the developer's tool loop can offer search_code. An unscoped query (no
// repo_ids) is expanded to every registered repo, mirroring cmd/mcp-gateway's
// searcher, since retrieval.Search only fans out across the repo ids it's
// explicitly given.
type codeagentSearcher struct {
	g   *retrieval.Gateway
	reg *registry.Service
}

func (s codeagentSearcher) Search(ctx context.Context, query string, repoIDs []string, k int) ([]codeagent.SearchHit, error) {
	ids := repoIDs
	if len(ids) == 0 {
		repos, err := s.reg.List(ctx, registry.ListFilter{})
		if err != nil {
			return nil, fmt.Errorf("codeagent searcher: list repos for unscoped search: %w", err)
		}
		if len(repos) == 0 {
			return nil, nil
		}
		ids = make([]string, len(repos))
		for i, r := range repos {
			ids[i] = r.ID
		}
	}

	resp, err := s.g.Search(ctx, retrieval.SearchRequest{Query: query, RepoIDs: ids, FinalTopK: k})
	if err != nil {
		return nil, fmt.Errorf("codeagent searcher: search: %w", err)
	}
	hits := make([]codeagent.SearchHit, len(resp.Results))
	for i, r := range resp.Results {
		hits[i] = codeagent.SearchHit{RepoID: r.RepoID, Path: r.FilePath, Snippet: truncate(r.Content, maxSnippetLen), Score: r.Score}
	}
	return hits, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// resolveRoster merges the built-in per-role defaults with the operator's
// config `roles` overrides (only non-empty fields override).
func resolveRoster(overrides map[string]config.RoleConfig) map[string]worker.RoleParams {
	roster := worker.DefaultRoster()
	for role, oc := range overrides {
		p := roster[role] // zero value for an unknown role name
		if oc.Model != "" {
			p.Model = oc.Model
		}
		if oc.MaxTokens > 0 {
			p.MaxTokens = oc.MaxTokens
		}
		if oc.Thinking != nil {
			p.Thinking = *oc.Thinking
		}
		roster[role] = p
	}
	return roster
}

// resolveRoleConcurrency merges the built-in per-role in-flight caps with the
// operator's `worker.role_concurrency` overrides. An override with a positive
// value replaces the default; a value <=0 removes the cap (unbounded) for that
// role. Roles the operator doesn't mention keep their default.
func resolveRoleConcurrency(overrides map[string]int) map[string]int {
	limits := worker.DefaultRoleConcurrency()
	for role, n := range overrides {
		if n > 0 {
			limits[role] = n
		} else {
			delete(limits, role) // explicit 0/negative ⇒ unbounded
		}
	}
	return limits
}

// repoDirectory is a worker.RepoDirectory over the registry (names) + the
// ingestion context store (per-repo context cards).
type repoDirectory struct {
	reg *registry.Service
	cs  *ingestion.ContextStore
}

func (d repoDirectory) List(ctx context.Context, excerptBytes int) ([]worker.RepoInfo, error) {
	repos, err := d.reg.List(ctx, registry.ListFilter{})
	if err != nil {
		return nil, fmt.Errorf("repo directory list: %w", err)
	}
	out := make([]worker.RepoInfo, 0, len(repos))
	for _, r := range repos {
		card, _ := d.cs.LoadActive(ctx, r.ID) // best-effort; "" if none
		ex := card
		if len(ex) > excerptBytes {
			ex = ex[:excerptBytes]
		}
		out = append(out, worker.RepoInfo{ID: r.ID, Name: r.Name, Excerpt: ex})
	}
	return out, nil
}

func (d repoDirectory) Card(ctx context.Context, repoID string) (string, error) {
	return d.cs.LoadActive(ctx, repoID)
}

func main() {
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

	checkpoints, err := worker.NewCheckpointStore(ctx, pool)
	if err != nil {
		log.Fatalf("init checkpoints: %v", err)
	}

	regStore, err := registry.NewPGStore(ctx, pool)
	if err != nil {
		log.Fatalf("init registry store: %v", err)
	}
	reg := registry.NewService(regStore)

	// Embedder/reranker: local-first backends if configured, else deterministic
	// fakes (mirrors cmd/mcp-gateway and cmd/retrieval-gateway).
	var emb embedder.Embedder = embedder.NewFake(models.DefaultEmbeddingDim)
	if base := cfg.Embeddings.OllamaURL; base != "" {
		model := cfg.Embeddings.OllamaModel
		if model == "" {
			model = "nomic-embed-text"
		}
		emb = embedder.NewOllama(base, model, models.DefaultEmbeddingDim)
	}
	rr := reranker.New(cfg.Reranker.Kind, cfg.Reranker.URL, cfg.Reranker.Model)
	retGW := retrieval.NewGateway(pool, reg, emb, rr)

	// Anthropic backend by default; fake only when explicitly requested (local dev).
	var backend llm.Backend
	switch {
	case cfg.Worker.FakeLLM:
		backend = llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
			return llm.Response{Text: `{"summary":"stub","preliminary_complexity":"standard","affected_repos":[]}`}, nil
		})
	case cfg.Anthropic.Provider == "vertex":
		if cfg.Anthropic.VertexProject == "" || cfg.Anthropic.VertexRegion == "" {
			log.Fatal("vertex provider requires anthropic.vertex_project and anthropic.vertex_region")
		}
		backend = llm.NewAnthropic(
			llm.WithVertex(ctx, cfg.Anthropic.VertexRegion, cfg.Anthropic.VertexProject),
			llm.WithModelOverrides(cfg.Anthropic.ModelOverrides),
			llm.WithFeatureToggles(cfg.Anthropic.DisableThinking, cfg.Anthropic.DisableStructuredOutput),
		)
		log.Printf("worker: LLM backend = vertex (%s/%s)", cfg.Anthropic.VertexRegion, cfg.Anthropic.VertexProject)
	case cfg.Anthropic.APIKey != "":
		backend = llm.NewAnthropic(llm.WithAPIKey(cfg.Anthropic.APIKey))
	default:
		backend = llm.NewAnthropic()
	}
	sm := worker.NewSessionManager(backend, worker.SessionConfig{})

	producer, err := kafka.NewProducer(brokers, kafka.WithAuth(kafkaAuth))
	if err != nil {
		log.Fatalf("init producer: %v", err)
	}
	defer producer.Close()

	consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers: brokers, Group: "worker", Topics: []string{kafka.TopicStageRequested},
		MaxAttempts: cfg.Kafka.ConsumerMaxRetries, Auth: kafkaAuth,
		Concurrency: cfg.Worker.Concurrency,
	})
	if err != nil {
		log.Fatalf("init consumer: %v", err)
	}
	defer consumer.Close()

	// Resolve per-role LLM params: built-in defaults overridden by config `roles`.
	roster := resolveRoster(cfg.Roles)

	var developer worker.StageHandler
	switch cfg.Worker.DeveloperBackend {
	case "ma":
		log.Printf("worker: developer backend = ma (Managed Agents)")
		maClient := managedagent.NewHTTPMAClient(
			cfg.Anthropic.APIKey,
			cfg.ManagedAgents.APIBaseURL,
			cfg.ManagedAgents.AgentID,
			cfg.ManagedAgents.VaultCredentialRef,
		)
		developer = worker.NewManagedDeveloper(maClient, &plainURLResolver{reg: reg}, cfg.ManagedAgents.Model)
	default:
		log.Printf("worker: developer backend = local")
		// Fail-closed: the local developer runs untrusted commands (agent
		// run_command + repo `go test`). Require a jail prefix, or an explicit
		// dev-only opt-out. See redemption.md (C1).
		sbx := cfg.Worker.Sandbox
		if len(sbx.ExecPrefix) == 0 && !sbx.AllowUnsandboxed {
			log.Fatal("local developer sandbox is unsafe unsandboxed: set worker.sandbox.exec_prefix (a jail, e.g. bwrap/firejail with --net none) or worker.sandbox.allow_unsandboxed=true for dev/test only")
		}
		if len(sbx.ExecPrefix) == 0 {
			log.Printf("worker: WARNING developer sandbox running UNSANDBOXED (allow_unsandboxed=true) — dev/test only")
		}
		dp, ep, cp := roster[worker.RoleDeveloper], roster[worker.RoleDeveloperEscalation], roster[worker.RoleDeveloperCheap]
		draftAgent := codeagent.NewLocalToolLoopAgent(backend, dp.Model).WithMaxTokens(dp.MaxTokens).
			WithSearcher(codeagent.NewCachingSearcher(codeagentSearcher{g: retGW, reg: reg}))
		escAgent := codeagent.NewLocalToolLoopAgent(backend, ep.Model).WithMaxTokens(ep.MaxTokens).
			WithSearcher(codeagent.NewCachingSearcher(codeagentSearcher{g: retGW, reg: reg}))
		// Cheapest tier for trivial changes (per-file complexity routing).
		cheapAgent := codeagent.NewLocalToolLoopAgent(backend, cp.Model).WithMaxTokens(cp.MaxTokens).
			WithSearcher(codeagent.NewCachingSearcher(codeagentSearcher{g: retGW, reg: reg}))
		developer = worker.NewDeveloper(draftAgent, &registryResolver{reg: reg}).
			WithEscalation(escAgent).
			WithCheapAgent(cheapAgent).
			WithExecPrefix(sbx.ExecPrefix)
	}

	// Repo directory: registry names + per-repo context cards (ingested docs),
	// grounding repo selection (analyzer) and design (architect/designer).
	cstore := ingestion.NewContextStore(pool)
	if err := cstore.EnsureSchema(ctx); err != nil {
		log.Fatalf("ensure context schema: %v", err)
	}
	dir := repoDirectory{reg: reg, cs: cstore}

	p := worker.NewPool(consumer, producer, checkpoints, []worker.StageHandler{
		worker.NewRequirementAnalyzer(sm).WithRepoDirectory(dir).WithParams(roster[worker.RoleRequirementAnalyzer]),
		worker.NewViabilityChecker(sm).WithParams(roster[worker.RoleViabilityChecker]),
		worker.NewRiskAnalyzer(sm).WithParams(roster[worker.RoleRiskAnalyzer]),
		worker.NewSystemArchitect(sm).WithRepoDirectory(dir).WithParams(roster[worker.RoleSystemArchitect]),
		worker.NewSystemDesigner(sm).WithRepoDirectory(dir).WithParams(roster[worker.RoleSystemDesigner]),
		developer,
		worker.NewQA(sm).WithParams(roster[worker.RoleQA]),
	}).WithRoleLimits(resolveRoleConcurrency(cfg.Worker.RoleConcurrency))

	log.Printf("worker consuming %s on %v (concurrency=%d, role_limits=%v)",
		kafka.TopicStageRequested, brokers, cfg.Worker.Concurrency, resolveRoleConcurrency(cfg.Worker.RoleConcurrency))
	if err := p.Run(ctx); err != nil {
		log.Fatalf("run: %v", err)
	}
}
