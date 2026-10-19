// Command orchestrator runs the saga engine: it consumes run/stage events and
// drives run state transitions, emitting the next stage request or deliver.pr.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/signal"
	"syscall"

	"github.com/fin/multi-agent-rag/internal/config"
	"github.com/fin/multi-agent-rag/internal/events"
	"github.com/fin/multi-agent-rag/internal/gitprovider"
	"github.com/fin/multi-agent-rag/internal/kafka"
	"github.com/fin/multi-agent-rag/internal/orchestrator"
	"github.com/fin/multi-agent-rag/internal/publisher"
	"github.com/fin/multi-agent-rag/internal/registry"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

	store, err := orchestrator.NewStore(ctx, pool)
	if err != nil {
		log.Fatalf("init store: %v", err)
	}
	prod, err := kafka.NewProducer(brokers, kafka.WithAuth(kafkaAuth))
	if err != nil {
		log.Fatalf("init producer: %v", err)
	}
	defer prod.Close()

	regStore, err := registry.NewPGStore(ctx, pool)
	if err != nil {
		log.Fatalf("init registry store: %v", err)
	}
	reg := registry.NewService(regStore)

	executor := publisher.NewCompensationExecutor(&registryRepoInfo{reg: reg}, providerFactory, tokenFor)
	engine := orchestrator.NewEngine(store, prod).WithCompensationExecutor(executor)

	consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers:     brokers,
		Group:       "orchestrator",
		Topics:      []string{kafka.TopicRunRequested, kafka.TopicStageCompleted, kafka.TopicStageFailed, kafka.TopicGateResume, kafka.TopicRunPROpened},
		MaxAttempts: cfg.Kafka.ConsumerMaxRetries,
		Auth:        kafkaAuth,
	})
	if err != nil {
		log.Fatalf("init consumer: %v", err)
	}
	defer consumer.Close()

	log.Printf("orchestrator consuming on %v", brokers)
	if err := consumer.Run(ctx, func(ctx context.Context, rec kafka.Record) error {
		return handle(ctx, engine, rec)
	}); err != nil {
		log.Fatalf("consume: %v", err)
	}
}

// handle dispatches a consumed record to the engine by topic + envelope type.
func handle(ctx context.Context, engine *orchestrator.Engine, rec kafka.Record) error {
	env, payload, err := events.Unmarshal(rec.Value)
	if err != nil {
		return events.Poison(err) // undecodable → DLQ, don't retry
	}
	switch rec.Topic {
	case kafka.TopicRunRequested:
		var p struct {
			Spec    string   `json:"spec"`
			RepoIDs []string `json:"repo_ids"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return events.Poison(fmt.Errorf("decode run.requested payload: %w", err))
		}
		return engine.StartRun(ctx, env.RunId, p.Spec, p.RepoIDs)
	case kafka.TopicStageCompleted:
		var p struct {
			Stage           string          `json:"stage"`
			Complexity      string          `json:"confirmed_complexity"`
			GateRecommended bool            `json:"gate_recommended"`
			GateReason      string          `json:"gate_reason"`
			QAPassed        bool            `json:"qa_passed"`
			ProposedRepos   []string        `json:"proposed_repos"`
			Artifact        json.RawMessage `json:"artifact"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return events.Poison(fmt.Errorf("decode run.stage.completed payload: %w", err))
		}
		return engine.OnStageCompleted(ctx, orchestrator.StageCompleted{
			RunID: env.RunId, Stage: orchestrator.Stage(p.Stage),
			ConfirmedComplexity: orchestrator.Complexity(p.Complexity),
			GateRecommended:     p.GateRecommended,
			GateReason:          p.GateReason,
			QAPassed:            p.QAPassed,
			ProposedRepos:       p.ProposedRepos,
			Artifact:            p.Artifact,
		})
	case kafka.TopicStageFailed:
		var p struct {
			Stage  string `json:"stage"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return events.Poison(fmt.Errorf("decode run.stage.failed payload: %w", err))
		}
		return engine.OnStageFailed(ctx, env.RunId, orchestrator.Stage(p.Stage), p.Reason)
	case kafka.TopicGateResume:
		var p struct {
			Approved bool `json:"approved"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return events.Poison(fmt.Errorf("decode gate.resume payload: %w", err))
		}
		return engine.ResumeGate(ctx, env.RunId, p.Approved)
	case kafka.TopicRunPROpened:
		var p struct {
			PRs []struct {
				RepoID string `json:"repo_id"`
				PRID   string `json:"pr_id"`
				URL    string `json:"pr_url"`
			} `json:"prs"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return events.Poison(fmt.Errorf("decode run.pr.opened payload: %w", err))
		}
		opened := make([]orchestrator.PRRef, len(p.PRs))
		for i, pr := range p.PRs {
			opened[i] = orchestrator.PRRef{RepoID: pr.RepoID, PRID: pr.PRID, URL: pr.URL}
		}
		return engine.OnPROpened(ctx, orchestrator.PROpened{RunID: env.RunId, Opened: opened})
	default:
		return nil // ignore unknown topics
	}
}

// registryRepoInfo adapts *registry.Service to publisher.RepoInfo.
type registryRepoInfo struct {
	reg *registry.Service
}

func (r *registryRepoInfo) Get(ctx context.Context, repoID string) (publisher.RepoMeta, error) {
	repo, err := r.reg.Get(ctx, repoID)
	if err != nil {
		return publisher.RepoMeta{}, fmt.Errorf("registry repo info: get %q: %w", repoID, err)
	}
	return publisher.RepoMeta{
		Provider:      string(repo.Provider),
		RemoteURL:     repo.RemoteURL,
		DefaultBranch: repo.DefaultBranch,
	}, nil
}

// providerFactory resolves a GitProvider backend by name.
func providerFactory(name string) (gitprovider.GitProvider, error) {
	switch name {
	case "github":
		return gitprovider.NewGitHub(nil), nil
	case "gitlab":
		return gitprovider.NewGitLab(nil), nil
	default:
		return nil, fmt.Errorf("orchestrator: unknown provider %q", name)
	}
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
