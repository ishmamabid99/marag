// Package gateway is the transport-agnostic trigger core shared by every entry
// adapter (MCP, Linear, Slack). It produces run.requested / gate.resume events
// and reads run/repo/context state via injected interfaces.
package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/fin/multi-agent-rag/internal/events"
	"github.com/fin/multi-agent-rag/internal/kafka"
)

// ErrEmptySpec is returned when SubmitRun is called without a spec.
var ErrEmptySpec = errors.New("gateway: empty spec")

// Producer publishes events (satisfied by *kafka.Producer).
type Producer interface {
	Publish(ctx context.Context, topic, key string, value []byte) error
}

// RunView is a gateway-facing snapshot of a run.
type RunView struct {
	RunID         string                     `json:"run_id"`
	Status        string                     `json:"status"`
	CurrentStage  string                     `json:"current_stage"`
	Complexity    string                     `json:"complexity"`
	Spec          string                     `json:"spec"`
	RepoIDs       []string                   `json:"repo_ids"`
	QAIterations  int                        `json:"qa_iterations"`
	FailureReason string                     `json:"failure_reason,omitempty"`
	Artifacts     map[string]json.RawMessage `json:"artifacts,omitempty"`
}

// RepoView is a gateway-facing repo summary.
type RepoView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	GitProvider string `json:"git_provider"`
	RemoteURL   string `json:"remote_url"`
}

// SearchHit is one cross-repo retrieval result.
type SearchHit struct {
	RepoID  string  `json:"repo_id"`
	Path    string  `json:"path"`
	Snippet string  `json:"snippet"`
	Score   float64 `json:"score"`
}

// RunReader reads run state (adapter over the orchestrator store).
type RunReader interface {
	GetRun(ctx context.Context, id string) (RunView, bool, error) // found=false → unknown/pending
	ListRuns(ctx context.Context, limit int) ([]RunView, error)
	LoadArtifacts(ctx context.Context, id string) (map[string][]byte, error)
}

// RepoLister lists registered repos (adapter over the registry).
type RepoLister interface {
	ListRepos(ctx context.Context) ([]RepoView, error)
}

// Searcher runs cross-repo semantic search + whole-file reads (adapter over the
// retrieval-gateway).
type Searcher interface {
	Search(ctx context.Context, query string, repoIDs []string, k int) ([]SearchHit, error)
	// GetFile returns a whole indexed file's content (found=false if not indexed).
	GetFile(ctx context.Context, repoID, filePath string) (content string, found bool, err error)
}

// Gateway is the trigger core.
type Gateway struct {
	prod   Producer
	runs   RunReader
	repos  RepoLister
	search Searcher
	newID  func() string
}

// New wires the core. Any read dependency may be nil if the caller only uses writes.
func New(prod Producer, runs RunReader, repos RepoLister, search Searcher) *Gateway {
	return &Gateway{prod: prod, runs: runs, repos: repos, search: search, newID: randomID}
}

// WithIDFunc overrides the run_id generator (for tests).
func (g *Gateway) WithIDFunc(fn func() string) *Gateway { g.newID = fn; return g }

func randomID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "run-" + hex.EncodeToString(b[:])
}

// SubmitRun starts a new run with a random id.
func (g *Gateway) SubmitRun(ctx context.Context, spec string, repoIDs []string) (string, error) {
	return g.SubmitRunKeyed(ctx, "", spec, repoIDs)
}

// SubmitRunKeyed starts a run. When key != "", the run_id is derived
// deterministically from it (hash), so a redelivered trigger for the same
// source event resolves to the same run (the orchestrator dedups on run_id).
func (g *Gateway) SubmitRunKeyed(ctx context.Context, key, spec string, repoIDs []string) (string, error) {
	if strings.TrimSpace(spec) == "" {
		return "", ErrEmptySpec
	}
	runID := g.newID()
	if key != "" {
		sum := sha256.Sum256([]byte(key))
		runID = "run-" + hex.EncodeToString(sum[:])[:32]
	}
	if err := g.produce(ctx, runID, "run.requested", kafka.TopicRunRequested, map[string]any{
		"spec": spec, "repo_ids": repoIDs,
	}); err != nil {
		return "", err
	}
	return runID, nil
}

// ResumeGate approves/rejects a run parked at a human gate.
func (g *Gateway) ResumeGate(ctx context.Context, runID string, approved bool) error {
	return g.produce(ctx, runID, "gate.resume", kafka.TopicGateResume, map[string]any{"approved": approved})
}

// RunStatus returns the current run snapshot (with artifacts). An unknown run
// is reported as pending rather than an error — it may not be consumed yet.
func (g *Gateway) RunStatus(ctx context.Context, runID string) (RunView, error) {
	v, found, err := g.runs.GetRun(ctx, runID)
	if err != nil {
		return RunView{}, fmt.Errorf("gateway: get run: %w", err)
	}
	if !found {
		return RunView{RunID: runID, Status: "pending"}, nil
	}
	arts, err := g.runs.LoadArtifacts(ctx, runID)
	if err != nil {
		return RunView{}, fmt.Errorf("gateway: load artifacts: %w", err)
	}
	if len(arts) > 0 {
		v.Artifacts = make(map[string]json.RawMessage, len(arts))
		for st, a := range arts {
			v.Artifacts[st] = json.RawMessage(a)
		}
	}
	return v, nil
}

// ListRuns returns recent runs.
func (g *Gateway) ListRuns(ctx context.Context, limit int) ([]RunView, error) {
	if limit <= 0 {
		limit = 20
	}
	runs, err := g.runs.ListRuns(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("gateway: list runs: %w", err)
	}
	return runs, nil
}

// ListRepos returns registered repos (the "context of all codebases").
func (g *Gateway) ListRepos(ctx context.Context) ([]RepoView, error) {
	repos, err := g.repos.ListRepos(ctx)
	if err != nil {
		return nil, fmt.Errorf("gateway: list repos: %w", err)
	}
	return repos, nil
}

// SearchContext runs cross-repo semantic search over registered repos.
func (g *Gateway) SearchContext(ctx context.Context, query string, repoIDs []string, k int) ([]SearchHit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("gateway: empty search query")
	}
	if k <= 0 {
		k = 10
	}
	hits, err := g.search.Search(ctx, query, repoIDs, k)
	if err != nil {
		return nil, fmt.Errorf("gateway: search: %w", err)
	}
	return hits, nil
}

// GetFile returns a whole indexed file's content from the knowledge base
// (found=false if the file isn't indexed for that repo).
func (g *Gateway) GetFile(ctx context.Context, repoID, filePath string) (string, bool, error) {
	if strings.TrimSpace(repoID) == "" || strings.TrimSpace(filePath) == "" {
		return "", false, fmt.Errorf("gateway: repo_id and file_path are required")
	}
	content, found, err := g.search.GetFile(ctx, repoID, filePath)
	if err != nil {
		return "", false, fmt.Errorf("gateway: get file: %w", err)
	}
	return content, found, nil
}

func (g *Gateway) produce(ctx context.Context, runID, typ, topic string, payload map[string]any) error {
	env, err := events.NewEnvelope(runID, typ, "gateway", 1)
	if err != nil {
		return fmt.Errorf("gateway: envelope: %w", err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("gateway: marshal %s: %w", typ, err)
	}
	value, err := events.Marshal(&env, body)
	if err != nil {
		return fmt.Errorf("gateway: marshal envelope: %w", err)
	}
	if err := g.prod.Publish(ctx, topic, runID, value); err != nil {
		return fmt.Errorf("gateway: publish %s: %w", typ, err)
	}
	return nil
}
