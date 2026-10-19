// Command linear-gateway serves a Linear webhook endpoint: a signature-verified
// @mention (or trigger phrase) on an issue/comment starts an autonomous run
// over the shared trigger core (internal/gateway), records the run_id ->
// issue_id mapping, and acknowledges back on the issue with the run id. It
// also consumes run.pr.opened and posts the opened PR links (and any failed
// repos) back as a comment on the originating issue, closing the loop.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/fin/multi-agent-rag/internal/config"
	"github.com/fin/multi-agent-rag/internal/events"
	"github.com/fin/multi-agent-rag/internal/gateway"
	"github.com/fin/multi-agent-rag/internal/kafka"
	"github.com/fin/multi-agent-rag/internal/linear"
	"github.com/jackc/pgx/v5/pgxpool"
)

// shutdownTimeout bounds how long graceful shutdown waits for in-flight
// webhook requests to finish.
const shutdownTimeout = 10 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := config.Get()

	signingSecret := cfg.Linear.SigningSecret
	apiKey := cfg.Linear.APIKey
	trigger := cfg.Linear.Trigger
	if trigger == "" {
		trigger = "@marag"
	}
	addr := cfg.Linear.Addr
	if addr == "" {
		addr = ":8080"
	}
	brokers := cfg.Kafka.Brokers
	dsn := cfg.Database.DSN()
	if signingSecret == "" || apiKey == "" || len(brokers) == 0 || dsn == "" {
		log.Fatal("linear.signing_secret, linear.api_key, kafka.brokers and database.url are required")
	}
	kafkaAuth := kafka.ClientAuth{
		SASLUser:    cfg.Kafka.Username,
		SASLPass:    cfg.Kafka.Password,
		TLSEnabled:  cfg.Kafka.TLSEnabled,
		Compression: cfg.Kafka.ProducerCompression,
		MaxRetries:  cfg.Kafka.ProducerMaxRetries,
	}

	prod, err := kafka.NewProducer(brokers, kafka.WithAuth(kafkaAuth))
	if err != nil {
		log.Fatalf("init producer: %v", err)
	}
	defer prod.Close()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()

	store, err := linear.NewPGRunStore(ctx, pool)
	if err != nil {
		log.Fatalf("init linear run store: %v", err)
	}

	// Only SubmitRun (a write) is needed for the trigger path; read
	// dependencies are nil per gateway.New's contract. MCP now lives in its own
	// mcp-gateway service (mcp.marag.fin.com), not mounted here.
	g := gateway.New(prod, nil, nil, nil)
	client := linear.NewHTTPClient(nil, "", apiKey)
	handler := linear.NewHandler(signingSecret, trigger, g, client).
		WithRunStore(store).WithResumer(g).WithAllowedActors(cfg.Linear.AllowedActors)
	notifier := linear.NewNotifier(store, client).WithArtifacts(linear.NewPGArtifactReader(pool))

	consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers:     brokers,
		Group:       "linear-gateway",
		Topics:      []string{kafka.TopicRunPROpened, kafka.TopicRunGated, kafka.TopicStageFailed},
		MaxAttempts: cfg.Kafka.ConsumerMaxRetries,
		Auth:        kafkaAuth,
	})
	if err != nil {
		log.Fatalf("init consumer: %v", err)
	}
	defer consumer.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", webhookHandler(handler))

	// ReadHeaderTimeout caps how long a client may take to send request headers
	// — a cheap Slowloris mitigation for this internet-facing webhook receiver.
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("linear-gateway: listen: %v", err)
		}
	}()
	log.Printf("linear-gateway: listening on %s (brokers=%v)", addr, brokers)

	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		log.Printf("linear-gateway: consuming %s, %s, %s on %v", kafka.TopicRunPROpened, kafka.TopicRunGated, kafka.TopicStageFailed, brokers)
		if err := consumer.Run(ctx, func(ctx context.Context, rec kafka.Record) error {
			switch rec.Topic {
			case kafka.TopicRunGated:
				return handleGated(ctx, notifier, rec)
			case kafka.TopicStageFailed:
				return handleFailed(ctx, notifier, rec)
			default: // run.pr.opened
				return handlePROpened(ctx, notifier, rec)
			}
		}); err != nil {
			log.Printf("linear-gateway: consumer stopped: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("linear-gateway: shutting down")
	shCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shCtx); err != nil {
		log.Printf("linear-gateway: shutdown: %v", err)
	}
	<-consumerDone
}

// prOpenedPayload decodes the run.pr.opened payload emitted by
// internal/publisher.emitOpened: {"prs":[{"repo_id","pr_id","pr_url"}],
// "failed":[{"repo_id","error"}]}.
type prOpenedPayload struct {
	Prs []struct {
		RepoID string `json:"repo_id"`
		URL    string `json:"pr_url"`
	} `json:"prs"`
	Failed []struct {
		RepoID string `json:"repo_id"`
		Error  string `json:"error"`
	} `json:"failed"`
}

// handlePROpened decodes a run.pr.opened record and posts the opened PR links
// (and any failed repos) back on the originating Linear issue via notifier.
// A decode failure is poisoned straight to the DLQ; a Notify failure (e.g. an
// unmapped run or a comment-post error) is logged and swallowed — the PR(s)
// already opened successfully, so there's nothing to retry here.
func handlePROpened(ctx context.Context, notifier *linear.Notifier, rec kafka.Record) error {
	env, payload, err := events.Unmarshal(rec.Value)
	if err != nil {
		return events.Poison(err) // undecodable → DLQ, don't retry
	}
	var evt prOpenedPayload
	if err := json.Unmarshal(payload, &evt); err != nil {
		return events.Poison(fmt.Errorf("decode run.pr.opened payload: %w", err))
	}
	prs := make([]linear.OpenedPR, 0, len(evt.Prs))
	for _, p := range evt.Prs {
		prs = append(prs, linear.OpenedPR{RepoID: p.RepoID, URL: p.URL})
	}
	failed := make([]linear.FailedRepo, 0, len(evt.Failed))
	for _, f := range evt.Failed {
		failed = append(failed, linear.FailedRepo{RepoID: f.RepoID, Error: f.Error})
	}
	if err := notifier.Notify(ctx, env.RunId, prs, failed); err != nil {
		log.Printf("linear-gateway: notify run=%s: %v", env.RunId, err)
	}
	return nil
}

// handleGated decodes a run.gated record ({stage, reason}) and posts a gate
// notification on the originating Linear issue via notifier. A decode failure
// is poisoned to the DLQ; a Notify failure (unmapped run or comment error) is
// logged and swallowed — there is nothing to retry.
func handleGated(ctx context.Context, notifier *linear.Notifier, rec kafka.Record) error {
	env, payload, err := events.Unmarshal(rec.Value)
	if err != nil {
		return events.Poison(err) // undecodable → DLQ, don't retry
	}
	var p struct {
		Stage  string `json:"stage"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return events.Poison(fmt.Errorf("decode run.gated payload: %w", err))
	}
	if err := notifier.NotifyGate(ctx, env.RunId, p.Stage, p.Reason); err != nil {
		log.Printf("linear-gateway: notify gate run=%s: %v", env.RunId, err)
	}
	return nil
}

// handleFailed decodes a run.stage.failed record ({stage, reason}) and posts a
// terminal-failure notice on the originating Linear issue via notifier. Decode
// failure → DLQ; a Notify failure (unmapped run or comment error) is logged and
// swallowed — the run already failed, nothing to retry.
func handleFailed(ctx context.Context, notifier *linear.Notifier, rec kafka.Record) error {
	env, payload, err := events.Unmarshal(rec.Value)
	if err != nil {
		return events.Poison(err) // undecodable → DLQ, don't retry
	}
	var p struct {
		Stage  string `json:"stage"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return events.Poison(fmt.Errorf("decode run.stage.failed payload: %w", err))
	}
	if err := notifier.NotifyFailure(ctx, env.RunId, p.Stage, p.Reason); err != nil {
		log.Printf("linear-gateway: notify failure run=%s: %v", env.RunId, err)
	}
	return nil
}

// webhookHandler builds the POST /webhook http.HandlerFunc: read the raw body,
// verify + process via handler.Handle, and map the result to a status code.
// A bad/missing signature (linear.ErrBadSignature) is the only case that maps
// to 401. If a run was already submitted (triggered && runID != ""), any
// post-submit error (e.g. the ack comment failing) still responds 200 — a 500
// there would make Linear retry the webhook and double-submit the run. Every
// other handler error (parse, pre-submit) is a 500. Never log the raw body or
// any secret.
func webhookHandler(handler *linear.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			log.Printf("linear-gateway: read body: %v", err)
			http.Error(w, "read body failed", http.StatusInternalServerError)
			return
		}

		sig := r.Header.Get("Linear-Signature")
		triggered, runID, err := handler.Handle(r.Context(), sig, body)
		switch {
		case errors.Is(err, linear.ErrBadSignature):
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		case triggered && runID != "":
			// The run was already submitted; only the ack comment failed (or
			// some other non-fatal follow-up error occurred). Do not 500 —
			// that would make Linear retry the webhook and double-submit the
			// run. Log the runID and error, then respond 200.
			if err != nil {
				log.Printf("linear-gateway: run %q submitted but post-submit step failed: %v", runID, err)
			}
			w.WriteHeader(http.StatusOK)
			return
		case err != nil:
			log.Printf("linear-gateway: webhook error: %v", err)
			http.Error(w, "webhook processing failed", http.StatusInternalServerError)
			return
		default:
			log.Printf("linear-gateway: webhook ok: triggered=%v runID=%q", triggered, runID)
			w.WriteHeader(http.StatusOK)
		}
	}
}
