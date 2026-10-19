// Command git-publisher consumes deliver.pr events and opens a PR/MR from the
// developer's pushed branch via the repo's configured GitProvider, registering
// compensations (close PR + delete branch) and emitting run.pr.opened.
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
		log.Fatalf("init orchestrator store: %v", err)
	}

	regStore, err := registry.NewPGStore(ctx, pool)
	if err != nil {
		log.Fatalf("init registry store: %v", err)
	}
	reg := registry.NewService(regStore)

	prod, err := kafka.NewProducer(brokers, kafka.WithAuth(kafkaAuth))
	if err != nil {
		log.Fatalf("init producer: %v", err)
	}
	defer prod.Close()

	pub := publisher.New(
		&registryRepoInfo{reg: reg},
		providerFactory,
		tokenFor,
		&storeCompensator{store: store},
		prod,
	).WithClaimer(storeClaimer{s: store})

	consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers:     brokers,
		Group:       "git-publisher",
		Topics:      []string{kafka.TopicDeliverPR},
		MaxAttempts: cfg.Kafka.ConsumerMaxRetries,
		Auth:        kafkaAuth,
	})
	if err != nil {
		log.Fatalf("init consumer: %v", err)
	}
	defer consumer.Close()

	log.Printf("git-publisher consuming %s on %v", kafka.TopicDeliverPR, brokers)
	if err := consumer.Run(ctx, func(ctx context.Context, rec kafka.Record) error {
		return handle(ctx, pub, rec)
	}); err != nil {
		log.Fatalf("consume: %v", err)
	}
}

// handle decodes a deliver.pr record and drives the publisher.
func handle(ctx context.Context, pub *publisher.Publisher, rec kafka.Record) error {
	env, payload, err := events.Unmarshal(rec.Value)
	if err != nil {
		return events.Poison(err) // undecodable → DLQ, don't retry
	}
	if rec.Topic != kafka.TopicDeliverPR {
		return nil // ignore unknown topics
	}
	var evt publisher.DeliverEvent
	if err := json.Unmarshal(payload, &evt); err != nil {
		return events.Poison(fmt.Errorf("decode deliver.pr payload: %w", err))
	}
	evt.RunID = env.RunId
	opened, err := pub.Deliver(ctx, evt)
	if err != nil {
		log.Printf("git-publisher: deliver run=%s: %v", env.RunId, err)
		return fmt.Errorf("git-publisher: deliver run=%s: %w", env.RunId, err)
	}
	for _, pr := range opened {
		log.Printf("git-publisher: opened PR run=%s repo=%s url=%s", env.RunId, pr.RepoID, pr.URL)
	}
	if len(opened) < len(evt.Repos) {
		log.Printf("git-publisher: %d of %d repos failed to deliver run=%s", len(evt.Repos)-len(opened), len(evt.Repos), env.RunId)
	}
	return nil
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

// storeCompensator adapts *orchestrator.Store to publisher.Compensator.
type storeCompensator struct {
	store *orchestrator.Store
}

func (s *storeCompensator) Register(ctx context.Context, runID string, comps []publisher.Comp) error {
	for _, c := range comps {
		// publisher.Comp carries no provider field (internal/publisher may not
		// import the registry), so GitProvider can't be recovered here; store
		// empty. Compensation EXECUTION (a follow-up, see plan's deferred
		// notes) resolves the provider from the repo_id already embedded in
		// ParamsJSON via the registry at rollback time.
		comp := orchestrator.Compensation{
			RunID:      runID,
			Stage:      orchestrator.StageDeveloping,
			Action:     c.Action,
			ParamsJSON: c.ParamsJSON,
		}
		if err := s.store.RegisterCompensation(ctx, comp); err != nil {
			return fmt.Errorf("store compensator: register %s: %w", c.Action, err)
		}
	}
	return nil
}

// storeClaimer adapts *orchestrator.Store to publisher.Claimer, so a
// redelivered deliver.pr is skipped instead of opening a duplicate PR.
type storeClaimer struct {
	s *orchestrator.Store
}

func (c storeClaimer) Claim(ctx context.Context, key string) (bool, error) {
	claimed, err := c.s.ClaimIdempotency(ctx, key)
	if err != nil {
		return false, fmt.Errorf("store claimer: claim %q: %w", key, err)
	}
	return claimed, nil
}

func (c storeClaimer) Release(ctx context.Context, key string) error {
	if err := c.s.ReleaseIdempotency(ctx, key); err != nil {
		return fmt.Errorf("store claimer: release %q: %w", key, err)
	}
	return nil
}

// providerFactory resolves a GitProvider backend by name.
func providerFactory(name string) (gitprovider.GitProvider, error) {
	switch name {
	case "github":
		return gitprovider.NewGitHub(nil), nil
	case "gitlab":
		return gitprovider.NewGitLab(nil), nil
	default:
		return nil, fmt.Errorf("git-publisher: unknown provider %q", name)
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
