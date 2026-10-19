package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fin/multi-agent-rag/internal/events"
	"github.com/fin/multi-agent-rag/internal/kafka"
)

// retryTransient retries fn up to attempts times with linear backoff, aborting
// on ctx cancellation. Used for idempotent writes (the checkpoint upsert) so a
// transient DB blip doesn't discard completed, paid work by forcing the whole
// (expensive) stage to re-run on redelivery (M6).
func retryTransient(ctx context.Context, attempts int, base time.Duration, fn func() error) error {
	var err error
	for i := 1; i <= attempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		if i < attempts {
			select {
			case <-time.After(time.Duration(i) * base):
			case <-ctx.Done():
				return err
			}
		}
	}
	return err
}

// Pool consumes run.stage.requested, dispatches to role handlers, checkpoints,
// and emits run.stage.completed / run.stage.failed.
//
// Concurrency (M3): the consumer dispatches a poll batch's records in parallel;
// this pool additionally bounds how many stages of each role run at once via a
// per-role semaphore (sems). Limits govern cost and memory — an expensive Opus
// developer stage is typically capped at 1, while cheap classification roles run
// several at a time — so one slow stage never head-of-line-blocks another run.
type Pool struct {
	consumer    *kafka.Consumer
	producer    *kafka.Producer
	checkpoints *CheckpointStore
	handlers    map[string]StageHandler
	sems        map[string]chan struct{} // per-role limiter; nil/absent ⇒ unbounded
}

// NewPool indexes the handlers by role.
func NewPool(consumer *kafka.Consumer, producer *kafka.Producer, checkpoints *CheckpointStore, handlers []StageHandler) *Pool {
	idx := make(map[string]StageHandler, len(handlers))
	for _, h := range handlers {
		idx[h.Role()] = h
	}
	return &Pool{consumer: consumer, producer: producer, checkpoints: checkpoints, handlers: idx}
}

// WithRoleLimits caps concurrent in-flight stages per role. A role mapped to n>0
// runs at most n at a time; roles absent from the map (or mapped to <=0) run
// unbounded. Returns the pool for chaining.
func (p *Pool) WithRoleLimits(limits map[string]int) *Pool {
	if len(limits) == 0 {
		return p
	}
	sems := make(map[string]chan struct{}, len(limits))
	for role, n := range limits {
		if n > 0 {
			sems[role] = make(chan struct{}, n)
		}
	}
	p.sems = sems
	return p
}

// acquire blocks until a slot for role is free (or ctx is cancelled), returning
// a release func. When the role is unbounded it returns a no-op release.
func (p *Pool) acquire(ctx context.Context, role string) (func(), error) {
	sem := p.sems[role]
	if sem == nil {
		return func() {}, nil
	}
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type stageRequestPayload struct {
	Role           string                     `json:"role"`
	Stage          string                     `json:"stage"`
	Spec           string                     `json:"spec"`
	RepoIDs        []string                   `json:"repo_ids"`
	PriorArtifacts map[string]json.RawMessage `json:"prior_artifacts"`
	Attempt        int                        `json:"attempt"`
}

// Run consumes until ctx is cancelled.
func (p *Pool) Run(ctx context.Context) error {
	return p.consumer.Run(ctx, p.handle)
}

func (p *Pool) handle(ctx context.Context, rec kafka.Record) error {
	env, payload, err := events.Unmarshal(rec.Value)
	if err != nil {
		return events.Poison(fmt.Errorf("decode stage.requested: %w", err))
	}
	var req stageRequestPayload
	if err := json.Unmarshal(payload, &req); err != nil {
		return events.Poison(fmt.Errorf("decode stage payload: %w", err))
	}
	handler, ok := p.handlers[req.Role]
	if !ok {
		return events.Poison(fmt.Errorf("no handler for role %q", req.Role))
	}

	attempt := req.Attempt
	if attempt < 1 {
		attempt = 1 // absent/zero → first attempt (backward compatible)
	}

	// Idempotency: a checkpoint (completed or failed) means re-emit the same
	// outcome without re-running the handler on redelivery.
	if cp, found, err := p.checkpoints.Load(ctx, env.RunId, req.Stage, attempt); err != nil {
		return fmt.Errorf("load checkpoint: %w", err)
	} else if found && cp.Status == "completed" {
		return p.emitCompleted(ctx, env.RunId, req.Stage, cp.ArtifactJSON)
	} else if found && cp.Status == "failed" {
		return p.emitFailed(ctx, env.RunId, req.Stage, cp.Reason)
	}

	prior := make(map[string][]byte, len(req.PriorArtifacts))
	for st, a := range req.PriorArtifacts {
		prior[st] = []byte(a)
	}

	// Bound concurrent work per role (cost/memory governor). Acquired only for
	// the actual handler run — the checkpoint short-circuit above never waits on
	// a slot. A cancellation here is transient (redeliver), not a terminal fail.
	release, err := p.acquire(ctx, req.Role)
	if err != nil {
		return fmt.Errorf("worker: cancelled awaiting %q slot: %w", req.Role, err)
	}
	defer release()

	in := StageInput{RunID: env.RunId, Stage: req.Stage, Spec: req.Spec, RepoIDs: req.RepoIDs, Attempt: attempt, PriorArtifacts: prior}
	res, herr := handler.Handle(ctx, in)
	if herr != nil {
		// Transient failures (breaker open, context cancel/deadline) must NOT be
		// checkpointed terminal — return the error so the record is redelivered/retried.
		if errors.Is(herr, ErrCircuitOpen) || ctx.Err() != nil {
			return fmt.Errorf("worker: transient stage failure (retryable): %w", herr)
		}
		// Deterministic failure (e.g. parse/validation) is terminal.
		failedCP := Checkpoint{RunID: env.RunId, Stage: req.Stage, Attempt: attempt, Status: "failed", Reason: herr.Error()}
		if err := retryTransient(ctx, 3, 100*time.Millisecond, func() error { return p.checkpoints.Save(ctx, failedCP) }); err != nil {
			return fmt.Errorf("save failed checkpoint: %w", err)
		}
		return p.emitFailed(ctx, env.RunId, req.Stage, herr.Error())
	}

	completedCP := Checkpoint{
		RunID: env.RunId, Stage: req.Stage, Attempt: attempt, Status: "completed",
		ArtifactJSON: res.ArtifactJSON, Usage: res.Usage,
	}
	if err := retryTransient(ctx, 3, 100*time.Millisecond, func() error { return p.checkpoints.Save(ctx, completedCP) }); err != nil {
		return fmt.Errorf("save checkpoint: %w", err)
	}
	return p.emitCompleted(ctx, env.RunId, req.Stage, res.ArtifactJSON)
}

func (p *Pool) emitCompleted(ctx context.Context, runID, stage string, artifact []byte) error {
	env, err := events.NewEnvelope(runID, "stage.completed", "worker", 1)
	if err != nil {
		return fmt.Errorf("emit completed: %w", err)
	}
	// Promote routing fields from the artifact generically (works for any role
	// and for the re-emit-from-checkpoint path, which stores the artifact).
	var promo struct {
		Preliminary     string   `json:"preliminary_complexity"`
		Confirmed       string   `json:"confirmed_complexity"`
		GateRecommended bool     `json:"gate_recommended"`
		GateReason      string   `json:"gate_reason"`
		QAPassed        *bool    `json:"qa_passed"`
		ProposedRepos   []string `json:"proposed_repos"`
	}
	_ = json.Unmarshal(artifact, &promo)
	fields := map[string]any{"stage": stage, "artifact": json.RawMessage(artifact)}
	if promo.Preliminary != "" {
		fields["preliminary_complexity"] = promo.Preliminary
	}
	if promo.Confirmed != "" {
		fields["confirmed_complexity"] = promo.Confirmed
	}
	if promo.GateRecommended {
		fields["gate_recommended"] = true
	}
	if promo.GateReason != "" {
		fields["gate_reason"] = promo.GateReason
	}
	if promo.QAPassed != nil {
		fields["qa_passed"] = *promo.QAPassed
	}
	if len(promo.ProposedRepos) > 0 {
		fields["proposed_repos"] = promo.ProposedRepos
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		return fmt.Errorf("emit completed: marshal: %w", err)
	}
	value, err := events.Marshal(&env, payload)
	if err != nil {
		return fmt.Errorf("emit completed: %w", err)
	}
	if err := p.producer.Publish(ctx, kafka.TopicStageCompleted, runID, value); err != nil {
		return fmt.Errorf("emit completed publish: %w", err)
	}
	return nil
}

func (p *Pool) emitFailed(ctx context.Context, runID, stage, reason string) error {
	env, err := events.NewEnvelope(runID, "stage.failed", "worker", 1)
	if err != nil {
		return fmt.Errorf("emit failed: %w", err)
	}
	payload, err := json.Marshal(map[string]string{"stage": stage, "reason": reason})
	if err != nil {
		return fmt.Errorf("emit failed: marshal: %w", err)
	}
	value, err := events.Marshal(&env, payload)
	if err != nil {
		return fmt.Errorf("emit failed: %w", err)
	}
	if err := p.producer.Publish(ctx, kafka.TopicStageFailed, runID, value); err != nil {
		return fmt.Errorf("emit failed publish: %w", err)
	}
	return nil
}
