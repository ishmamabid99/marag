package mcpgw

import (
	"context"
	"errors"
	"fmt"

	"github.com/fin/multi-agent-rag/internal/config"
	"github.com/fin/multi-agent-rag/internal/gateway"
	"github.com/fin/multi-agent-rag/internal/models"
	"github.com/fin/multi-agent-rag/internal/orchestrator"
	"github.com/fin/multi-agent-rag/internal/registry"
	"github.com/fin/multi-agent-rag/internal/retrieval"
	"github.com/fin/multi-agent-rag/pkg/embedder"
	"github.com/fin/multi-agent-rag/pkg/reranker"
	"github.com/jackc/pgx/v5/pgxpool"
)

// maxSnippetLen bounds a search_code snippet so one result can't blow up the
// response payload.
const maxSnippetLen = 1200

// BuildGateway constructs the full read/write gateway.Gateway — Kafka producer
// for triggers plus registry/retrieval/orchestrator read deps — from config, a
// pgx pool, and a producer. Shared by cmd/mcp-gateway (stdio) and
// cmd/linear-gateway (HTTP /mcp) so both expose identical MCP tool behavior.
func BuildGateway(ctx context.Context, cfg *config.Config, pool *pgxpool.Pool, prod gateway.Producer) (*gateway.Gateway, error) {
	store, err := orchestrator.NewStore(ctx, pool)
	if err != nil {
		return nil, fmt.Errorf("mcpgw: orchestrator store: %w", err)
	}
	regStore, err := registry.NewPGStore(ctx, pool)
	if err != nil {
		return nil, fmt.Errorf("mcpgw: registry store: %w", err)
	}
	reg := registry.NewService(regStore)

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

	return gateway.New(prod, &runReader{store: store}, &repoLister{reg: reg}, &searcher{gw: retGW, reg: reg}), nil
}

// runReader adapts *orchestrator.Store to gateway.RunReader.
type runReader struct{ store *orchestrator.Store }

func (r *runReader) GetRun(ctx context.Context, id string) (gateway.RunView, bool, error) {
	run, err := r.store.GetRun(ctx, id)
	if errors.Is(err, orchestrator.ErrRunNotFound) {
		return gateway.RunView{}, false, nil
	}
	if err != nil {
		return gateway.RunView{}, false, fmt.Errorf("run reader: get run: %w", err)
	}
	return toRunView(run), true, nil
}

func (r *runReader) ListRuns(ctx context.Context, limit int) ([]gateway.RunView, error) {
	runs, err := r.store.ListRuns(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("run reader: list runs: %w", err)
	}
	out := make([]gateway.RunView, len(runs))
	for i, run := range runs {
		out[i] = toRunView(run)
	}
	return out, nil
}

func (r *runReader) LoadArtifacts(ctx context.Context, id string) (map[string][]byte, error) {
	arts, err := r.store.LoadArtifacts(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("run reader: load artifacts: %w", err)
	}
	out := make(map[string][]byte, len(arts))
	for stage, b := range arts {
		out[string(stage)] = b
	}
	return out, nil
}

func toRunView(r orchestrator.Run) gateway.RunView {
	v := gateway.RunView{
		RunID: r.ID, Status: string(r.Status), CurrentStage: string(r.CurrentStage),
		Complexity: string(r.Complexity), Spec: r.Spec, RepoIDs: r.RepoIDs,
		QAIterations: r.QAIterations,
	}
	if r.FailureReason != nil {
		v.FailureReason = *r.FailureReason
	}
	return v
}

// repoLister adapts *registry.Service to gateway.RepoLister.
type repoLister struct{ reg *registry.Service }

func (l *repoLister) ListRepos(ctx context.Context) ([]gateway.RepoView, error) {
	repos, err := l.reg.List(ctx, registry.ListFilter{})
	if err != nil {
		return nil, fmt.Errorf("repo lister: list: %w", err)
	}
	out := make([]gateway.RepoView, len(repos))
	for i, r := range repos {
		out[i] = gateway.RepoView{ID: r.ID, Name: r.Name, GitProvider: string(r.Provider), RemoteURL: r.RemoteURL}
	}
	return out, nil
}

// searcher adapts *retrieval.Gateway to gateway.Searcher. An unscoped query is
// expanded to every registered repo (retrieval.Search only fans out over ids it
// is given).
type searcher struct {
	gw  *retrieval.Gateway
	reg *registry.Service
}

func (s *searcher) Search(ctx context.Context, query string, repoIDs []string, k int) ([]gateway.SearchHit, error) {
	ids := repoIDs
	if len(ids) == 0 {
		repos, err := s.reg.List(ctx, registry.ListFilter{})
		if err != nil {
			return nil, fmt.Errorf("searcher: list repos for unscoped search: %w", err)
		}
		if len(repos) == 0 {
			return nil, nil
		}
		ids = make([]string, len(repos))
		for i, r := range repos {
			ids[i] = r.ID
		}
	}
	resp, err := s.gw.Search(ctx, retrieval.SearchRequest{Query: query, RepoIDs: ids, FinalTopK: k})
	if err != nil {
		return nil, fmt.Errorf("searcher: search: %w", err)
	}
	out := make([]gateway.SearchHit, len(resp.Results))
	for i, res := range resp.Results {
		out[i] = gateway.SearchHit{RepoID: res.RepoID, Path: res.FilePath, Snippet: truncate(res.Content, maxSnippetLen), Score: res.Score}
	}
	return out, nil
}

func (s *searcher) GetFile(ctx context.Context, repoID, filePath string) (string, bool, error) {
	return s.gw.GetFile(ctx, repoID, filePath)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
