package worker

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fin/multi-agent-rag/internal/codeagent"
	"github.com/fin/multi-agent-rag/internal/events"
	"github.com/fin/multi-agent-rag/internal/kafka"
	"github.com/fin/multi-agent-rag/internal/sandbox"
	"github.com/fin/multi-agent-rag/pkg/llm"
	"github.com/testcontainers/testcontainers-go/modules/redpanda"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// startRedpanda + createTopics are defined in internal/kafka tests but not
// exported; this package needs its own copies. (Both packages share the same
// franz-go version.)
func startRedpanda(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	ctx := context.Background()
	ctr, err := redpanda.Run(ctx, "redpandadata/redpanda:v24.2.7")
	if err != nil {
		t.Fatalf("start redpanda: %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })
	broker, err := ctr.KafkaSeedBroker(ctx)
	if err != nil {
		t.Fatalf("seed broker: %v", err)
	}
	return broker
}

func createTopics(t *testing.T, broker string, topics ...string) {
	t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(broker))
	if err != nil {
		t.Fatalf("admin client: %v", err)
	}
	defer cl.Close()
	resp, err := kadm.NewClient(cl).CreateTopics(context.Background(), 1, 1, nil, topics...)
	if err != nil {
		t.Fatalf("create topics: %v", err)
	}
	for _, tr := range resp.Sorted() {
		if tr.Err != nil && !errorsIsTopicExists(tr.Err) {
			t.Fatalf("create topic %s: %v", tr.Topic, tr.Err)
		}
	}
}

func errorsIsTopicExists(err error) bool { return err == kerr.TopicAlreadyExists }

func readOne(t *testing.T, broker, topic string) *kgo.Record {
	t.Helper()
	cl, _ := kgo.NewClient(kgo.SeedBrokers(broker), kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	defer cl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	f := cl.PollFetches(ctx)
	if err := f.Err(); err != nil {
		t.Fatalf("poll %s: %v", topic, err)
	}
	recs := f.Records()
	if len(recs) == 0 {
		return nil
	}
	return recs[0]
}

func publishStageRequest(t *testing.T, p *kafka.Producer, runID, role, stage, spec string) {
	t.Helper()
	env, _ := events.NewEnvelope(runID, "stage.requested", "test", 1)
	payload, _ := json.Marshal(map[string]any{"role": role, "stage": stage, "spec": spec, "repo_ids": []string{"repo-a"}})
	value, _ := events.Marshal(&env, payload)
	if err := p.Publish(context.Background(), kafka.TopicStageRequested, runID, value); err != nil {
		t.Fatalf("publish stage request: %v", err)
	}
}

func TestPoolHandlesAndEmitsCompleted(t *testing.T) {
	ctx := context.Background()
	broker := startRedpanda(t)
	createTopics(t, broker, kafka.TopicStageRequested, kafka.TopicStageCompleted, kafka.TopicStageFailed)
	pool := newPool(t)

	prod, _ := kafka.NewProducer([]string{broker})
	defer prod.Close()

	backend := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: `{"summary":"s","preliminary_complexity":"trivial","affected_repos":["repo-a"]}`,
			Usage: llm.Usage{InputTokens: 5, OutputTokens: 2}}, nil
	})
	analyzer := NewRequirementAnalyzer(NewSessionManager(backend, SessionConfig{}))

	consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers: []string{broker}, Group: "worker-1", Topics: []string{kafka.TopicStageRequested},
	})
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	defer consumer.Close()

	p := NewPool(consumer, prod, pool.checkpoints, []StageHandler{analyzer})

	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = p.Run(runCtx) }()

	publishStageRequest(t, prod, "run-1", "requirement-analyzer", "analyzing", "add a feature")

	rec := readOne(t, broker, kafka.TopicStageCompleted)
	if rec == nil {
		t.Fatal("no completed event")
	}
	env, payload, err := events.Unmarshal(rec.Value)
	if err != nil {
		t.Fatalf("unmarshal completed: %v", err)
	}
	if env.RunId != "run-1" || env.Type != "stage.completed" {
		t.Fatalf("envelope = %+v", env)
	}
	var out struct {
		Stage      string `json:"stage"`
		Complexity string `json:"preliminary_complexity"`
	}
	_ = json.Unmarshal(payload, &out)
	if out.Stage != "analyzing" || out.Complexity != "trivial" {
		t.Fatalf("completed payload = %s", payload)
	}
	cancel()
	wg.Wait()

	// Checkpoint recorded.
	cp, ok, _ := pool.checkpoints.Load(ctx, "run-1", "analyzing", 1)
	if !ok || cp.Status != "completed" {
		t.Fatalf("checkpoint = %+v ok %v", cp, ok)
	}
}

func TestPoolUnknownRoleGoesToDLQ(t *testing.T) {
	ctx := context.Background()
	broker := startRedpanda(t)
	createTopics(t, broker, kafka.TopicStageRequested, kafka.DLQTopic(kafka.TopicStageRequested))
	pool := newPool(t)
	prod, _ := kafka.NewProducer([]string{broker})
	defer prod.Close()

	consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers: []string{broker}, Group: "worker-2", Topics: []string{kafka.TopicStageRequested},
		MaxAttempts: 2, BaseBackoff: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	defer consumer.Close()

	p := NewPool(consumer, prod, pool.checkpoints, nil) // no handlers → unknown role
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = p.Run(runCtx) }()

	publishStageRequest(t, prod, "run-x", "nonexistent-role", "analyzing", "spec")

	dlq := readOne(t, broker, kafka.DLQTopic(kafka.TopicStageRequested))
	if dlq == nil {
		t.Fatal("unknown-role record should reach the DLQ")
	}
	cancel()
	wg.Wait()
}

// readN reads exactly n records from the start of topic, blocking until all
// n have arrived or the timeout elapses.
func readN(t *testing.T, broker, topic string, n int) []*kgo.Record {
	t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(broker), kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatalf("reader client: %v", err)
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	var recs []*kgo.Record
	for len(recs) < n {
		f := cl.PollFetches(ctx)
		if err := f.Err(); err != nil {
			t.Fatalf("poll %s: %v", topic, err)
		}
		recs = append(recs, f.Records()...)
	}
	return recs
}

func TestPoolIdempotentRedelivery(t *testing.T) {
	ctx := context.Background()
	broker := startRedpanda(t)
	createTopics(t, broker, kafka.TopicStageRequested, kafka.TopicStageCompleted, kafka.TopicStageFailed)
	pool := newPool(t)

	prod, _ := kafka.NewProducer([]string{broker})
	defer prod.Close()

	backend := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: `{"summary":"s","preliminary_complexity":"trivial","affected_repos":["repo-a"]}`,
			Usage: llm.Usage{InputTokens: 5, OutputTokens: 2}}, nil
	})
	analyzer := NewRequirementAnalyzer(NewSessionManager(backend, SessionConfig{}))

	consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers: []string{broker}, Group: "worker-idem", Topics: []string{kafka.TopicStageRequested},
	})
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	defer consumer.Close()

	p := NewPool(consumer, prod, pool.checkpoints, []StageHandler{analyzer})

	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = p.Run(runCtx) }()

	publishStageRequest(t, prod, "run-idem", "requirement-analyzer", "analyzing", "add a feature")

	// Wait for the first completed record before publishing the duplicate, so
	// the completed checkpoint is guaranteed to exist for the redelivery.
	first := readOne(t, broker, kafka.TopicStageCompleted)
	if first == nil {
		t.Fatal("no completed event for first request")
	}

	publishStageRequest(t, prod, "run-idem", "requirement-analyzer", "analyzing", "add a feature")

	// Consume both completed records (first + re-emitted duplicate) from the
	// start of the topic to let the second publish settle before asserting.
	recs := readN(t, broker, kafka.TopicStageCompleted, 2)
	if len(recs) != 2 {
		t.Fatalf("got %d completed records, want 2", len(recs))
	}
	for _, rec := range recs {
		env, _, err := events.Unmarshal(rec.Value)
		if err != nil {
			t.Fatalf("unmarshal completed: %v", err)
		}
		if env.RunId != "run-idem" || env.Type != "stage.completed" {
			t.Fatalf("envelope = %+v", env)
		}
	}

	cancel()
	wg.Wait()

	if calls := backend.Calls(); len(calls) != 1 {
		t.Fatalf("handler invoked %d times, want 1 (redelivery should short-circuit)", len(calls))
	}
}

func TestPoolIdempotentRedeliveryFailed(t *testing.T) {
	ctx := context.Background()
	broker := startRedpanda(t)
	createTopics(t, broker, kafka.TopicStageRequested, kafka.TopicStageCompleted, kafka.TopicStageFailed)
	pool := newPool(t)

	prod, _ := kafka.NewProducer([]string{broker})
	defer prod.Close()

	// Non-JSON reply makes RequirementAnalyzer.Handle fail deterministically
	// (parse error), while the fake still records the call.
	backend := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: "not json"}, nil
	})
	analyzer := NewRequirementAnalyzer(NewSessionManager(backend, SessionConfig{}))

	consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers: []string{broker}, Group: "worker-idem-failed", Topics: []string{kafka.TopicStageRequested},
	})
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	defer consumer.Close()

	p := NewPool(consumer, prod, pool.checkpoints, []StageHandler{analyzer})

	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = p.Run(runCtx) }()

	publishStageRequest(t, prod, "run-idem-fail", "requirement-analyzer", "analyzing", "add a feature")

	// Wait for the first failed record before publishing the duplicate, so the
	// failed checkpoint is guaranteed to exist for the redelivery.
	first := readOne(t, broker, kafka.TopicStageFailed)
	if first == nil {
		t.Fatal("no failed event for first request")
	}
	_, firstPayload, err := events.Unmarshal(first.Value)
	if err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	var firstOut struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(firstPayload, &firstOut)
	if firstOut.Reason == "" {
		t.Fatalf("first failed payload has empty reason: %s", firstPayload)
	}

	publishStageRequest(t, prod, "run-idem-fail", "requirement-analyzer", "analyzing", "add a feature")

	// Consume both failed records (first + re-emitted duplicate) from the
	// start of the topic to let the second publish settle before asserting.
	recs := readN(t, broker, kafka.TopicStageFailed, 2)
	if len(recs) != 2 {
		t.Fatalf("got %d failed records, want 2", len(recs))
	}
	for _, rec := range recs {
		env, payload, err := events.Unmarshal(rec.Value)
		if err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}
		if env.RunId != "run-idem-fail" || env.Type != "stage.failed" {
			t.Fatalf("envelope = %+v", env)
		}
		var out struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(payload, &out)
		if out.Reason == "" {
			t.Fatalf("failed payload has empty reason: %s", payload)
		}
		if out.Reason != firstOut.Reason {
			t.Fatalf("reason = %q, want %q (same across redelivery)", out.Reason, firstOut.Reason)
		}
	}

	cancel()
	wg.Wait()

	// The analyzer validates + auto-retries a malformed reply up to
	// maxDecodeAttempts, so ONE Handle invocation makes that many LLM calls. The
	// redelivery must short-circuit on the failed checkpoint, adding zero — so
	// the total equals exactly one Handle's worth of retries.
	if calls := backend.Calls(); len(calls) != maxDecodeAttempts {
		t.Fatalf("LLM invoked %d times, want %d (one Handle's retries; redelivery must short-circuit)", len(calls), maxDecodeAttempts)
	}
}

// stubHandler is a minimal StageHandler for exercising pool error-handling
// paths without going through a real LLM session.
type stubHandler struct {
	role string
	err  error
}

func (h stubHandler) Role() string { return h.role }

func (h stubHandler) Handle(_ context.Context, _ StageInput) (StageResult, error) {
	return StageResult{}, h.err
}

func TestPoolTransientErrorNotCheckpointed(t *testing.T) {
	ctx := context.Background()
	broker := startRedpanda(t)
	createTopics(t, broker, kafka.TopicStageRequested, kafka.DLQTopic(kafka.TopicStageRequested))
	pool := newPool(t)
	prod, _ := kafka.NewProducer([]string{broker})
	defer prod.Close()

	consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers: []string{broker}, Group: "worker-transient", Topics: []string{kafka.TopicStageRequested},
		MaxAttempts: 2, BaseBackoff: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	defer consumer.Close()

	stub := stubHandler{role: "requirement-analyzer", err: ErrCircuitOpen}
	p := NewPool(consumer, prod, pool.checkpoints, []StageHandler{stub})

	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = p.Run(runCtx) }()

	publishStageRequest(t, prod, "run-transient", "requirement-analyzer", "analyzing", "spec")

	dlq := readOne(t, broker, kafka.DLQTopic(kafka.TopicStageRequested))
	if dlq == nil {
		t.Fatal("transient error should exhaust retries and reach the DLQ")
	}

	cancel()
	wg.Wait()

	// No terminal checkpoint should have been written for a transient failure.
	_, ok, err := pool.checkpoints.Load(ctx, "run-transient", "analyzing", 1)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}
	if ok {
		t.Fatal("transient error must not be checkpointed as terminal")
	}
}

func TestPoolViabilityEmitsConfirmedComplexity(t *testing.T) {
	ctx := context.Background()
	broker := startRedpanda(t)
	createTopics(t, broker, kafka.TopicStageRequested, kafka.TopicStageCompleted, kafka.TopicStageFailed)
	fx := newPool(t)
	prod, _ := kafka.NewProducer([]string{broker})
	defer prod.Close()

	backend := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: `{"feasible":true,"confirmed_complexity":"standard","feasibility_rationale":"ok"}`}, nil
	})
	checker := NewViabilityChecker(NewSessionManager(backend, SessionConfig{}))

	consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers: []string{broker}, Group: "worker-viab", Topics: []string{kafka.TopicStageRequested},
	})
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	defer consumer.Close()

	p := NewPool(consumer, prod, fx.checkpoints, []StageHandler{checker})
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = p.Run(runCtx) }()

	publishStageRequest(t, prod, "run-v", "viability-checker", "viability", "add a localized feature")

	rec := readOne(t, broker, kafka.TopicStageCompleted)
	if rec == nil {
		t.Fatal("no completed event")
	}
	_, payload, _ := events.Unmarshal(rec.Value)
	var out struct {
		Stage     string `json:"stage"`
		Confirmed string `json:"confirmed_complexity"`
	}
	_ = json.Unmarshal(payload, &out)
	if out.Stage != "viability" || out.Confirmed != "standard" {
		t.Fatalf("completed payload = %s", payload)
	}
	cancel()
	wg.Wait()
}

func TestPoolViabilityInfeasibleEmitsFailed(t *testing.T) {
	ctx := context.Background()
	broker := startRedpanda(t)
	createTopics(t, broker, kafka.TopicStageRequested, kafka.TopicStageCompleted, kafka.TopicStageFailed)
	fx := newPool(t)
	prod, _ := kafka.NewProducer([]string{broker})
	defer prod.Close()

	backend := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: `{"feasible":false,"confirmed_complexity":"complex","feasibility_rationale":"depends on a nonexistent service"}`}, nil
	})
	checker := NewViabilityChecker(NewSessionManager(backend, SessionConfig{}))

	consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers: []string{broker}, Group: "worker-viab-fail", Topics: []string{kafka.TopicStageRequested},
	})
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	defer consumer.Close()

	p := NewPool(consumer, prod, fx.checkpoints, []StageHandler{checker})
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = p.Run(runCtx) }()

	publishStageRequest(t, prod, "run-vf", "viability-checker", "viability", "impossible thing")

	rec := readOne(t, broker, kafka.TopicStageFailed)
	if rec == nil {
		t.Fatal("no failed event for infeasible run")
	}
	_, payload, _ := events.Unmarshal(rec.Value)
	var out struct {
		Stage  string `json:"stage"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(payload, &out)
	if out.Stage != "viability" || !strings.Contains(out.Reason, "infeasible") {
		t.Fatalf("failed payload = %s", payload)
	}
	cancel()
	wg.Wait()
}

func TestPoolReasoningRolesEmitCompleted(t *testing.T) {
	// A single superset JSON reply satisfies all three roles: each Handle only
	// unmarshals the fields its own artifact type declares.
	const reply = `{"blast_radius":"medium",
	 "decisions":[{"decision":"d","rationale":"r","scope":"s"}],
	 "file_changes":[{"file_path":"a.go","repo_id":"repo","change_type":"modify","description":"d","complexity_hint":"trivial"}]}`

	cases := []struct {
		role  string
		stage string
	}{
		{"risk-analyzer", "risk"},
		{"system-architect", "architecting"},
		{"system-designer", "designing"},
	}

	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			ctx := context.Background()
			broker := startRedpanda(t)
			createTopics(t, broker, kafka.TopicStageRequested, kafka.TopicStageCompleted, kafka.TopicStageFailed)
			fx := newPool(t)
			prod, _ := kafka.NewProducer([]string{broker})
			defer prod.Close()

			backend := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
				return llm.Response{Text: reply}, nil
			})
			sm := NewSessionManager(backend, SessionConfig{})

			handlers := []StageHandler{
				NewRiskAnalyzer(sm),
				NewSystemArchitect(sm),
				NewSystemDesigner(sm),
			}

			consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{
				Brokers: []string{broker}, Group: "worker-reasoning-" + tc.role, Topics: []string{kafka.TopicStageRequested},
			})
			if err != nil {
				t.Fatalf("consumer: %v", err)
			}
			defer consumer.Close()

			p := NewPool(consumer, prod, fx.checkpoints, handlers)
			runCtx, cancel := context.WithCancel(ctx)
			var wg sync.WaitGroup
			wg.Add(1)
			go func() { defer wg.Done(); _ = p.Run(runCtx) }()

			publishStageRequest(t, prod, "run-"+tc.role, tc.role, tc.stage, "add multi-currency support")

			rec := readOne(t, broker, kafka.TopicStageCompleted)
			if rec == nil {
				t.Fatal("no completed event")
			}
			env, payload, err := events.Unmarshal(rec.Value)
			if err != nil {
				t.Fatalf("unmarshal completed: %v", err)
			}
			if env.Type != "stage.completed" {
				t.Fatalf("envelope = %+v", env)
			}
			var out struct {
				Stage string `json:"stage"`
			}
			_ = json.Unmarshal(payload, &out)
			if out.Stage != tc.stage {
				t.Fatalf("completed payload = %s", payload)
			}

			cancel()
			wg.Wait()
		})
	}
}

func TestPoolPromotesGateRecommended(t *testing.T) {
	// risk-analyzer artifact with gate_recommended:true must surface as a
	// top-level gate_recommended field in the emitted run.stage.completed.
	ctx := context.Background()
	broker := startRedpanda(t)
	createTopics(t, broker, kafka.TopicStageRequested, kafka.TopicStageCompleted, kafka.TopicStageFailed)
	fx := newPool(t)
	prod, _ := kafka.NewProducer([]string{broker})
	defer prod.Close()

	const reply = `{"blast_radius":"critical","gate_recommended":true,"gate_reason":"touches money"}`
	backend := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: reply}, nil
	})
	sm := NewSessionManager(backend, SessionConfig{})

	consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers: []string{broker}, Group: "worker-gate", Topics: []string{kafka.TopicStageRequested},
	})
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	defer consumer.Close()

	p := NewPool(consumer, prod, fx.checkpoints, []StageHandler{NewRiskAnalyzer(sm)})
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = p.Run(runCtx) }()

	publishStageRequest(t, prod, "run-gate", "risk-analyzer", "risk", "add a payments feature")

	rec := readOne(t, broker, kafka.TopicStageCompleted)
	if rec == nil {
		t.Fatal("no completed event")
	}
	_, payload, err := events.Unmarshal(rec.Value)
	if err != nil {
		t.Fatalf("unmarshal completed: %v", err)
	}
	var out struct {
		Stage           string `json:"stage"`
		GateRecommended bool   `json:"gate_recommended"`
	}
	_ = json.Unmarshal(payload, &out)
	if out.Stage != "risk" || !out.GateRecommended {
		t.Fatalf("completed payload = %s", payload)
	}

	cancel()
	wg.Wait()
}

func TestPoolDeveloperEmitsCompleted(t *testing.T) {
	ctx := context.Background()
	broker := startRedpanda(t)
	createTopics(t, broker, kafka.TopicStageRequested, kafka.TopicStageCompleted, kafka.TopicStageFailed)
	fx := newPool(t)
	prod, _ := kafka.NewProducer([]string{broker})
	defer prod.Close()

	bare := seedBareRepo(t)
	agent := codeagent.NewFake(func(_ context.Context, _ codeagent.Task, sb sandbox.Sandbox) (codeagent.Outcome, error) {
		if err := sb.WriteFile("go.mod", []byte("module devp\n\ngo 1.21\n")); err != nil {
			return codeagent.Outcome{}, err
		}
		if err := sb.WriteFile("calc.go", []byte("package devp\n\nfunc Add(a, b int) int { return a + b }\n")); err != nil {
			return codeagent.Outcome{}, err
		}
		if err := sb.WriteFile("calc_test.go", []byte("package devp\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) { if Add(1, 1) != 2 { t.Fatal(\"x\") } }\n")); err != nil {
			return codeagent.Outcome{}, err
		}
		return codeagent.Outcome{Summary: "done"}, nil
	})
	dev := NewDeveloper(agent, &FakeResolver{Checkouts: map[string]RepoCheckout{"repo-a": {CloneURL: bare, BaseBranch: "main"}}})

	consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers: []string{broker}, Group: "worker-dev", Topics: []string{kafka.TopicStageRequested},
	})
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	defer consumer.Close()

	p := NewPool(consumer, prod, fx.checkpoints, []StageHandler{dev})
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = p.Run(runCtx) }()

	publishStageRequest(t, prod, "run-dev", "developer", "developing", "add Add(a,b)")

	rec := readOne(t, broker, kafka.TopicStageCompleted)
	if rec == nil {
		t.Fatal("no completed event")
	}
	env, payload, err := events.Unmarshal(rec.Value)
	if err != nil {
		t.Fatalf("unmarshal completed: %v", err)
	}
	if env.Type != "stage.completed" {
		t.Fatalf("envelope = %+v", env)
	}
	if !strings.Contains(string(payload), `"tests_passed":true`) {
		t.Fatalf("completed payload = %s", payload)
	}
	var out struct {
		Stage string `json:"stage"`
	}
	_ = json.Unmarshal(payload, &out)
	if out.Stage != "developing" {
		t.Fatalf("completed payload = %s", payload)
	}

	cancel()
	wg.Wait()
}

// priorArtifactsStub is a minimal StageHandler that records the
// StageInput.PriorArtifacts it was invoked with, so a test can assert the
// pool threaded prior_artifacts from the payload into the StageInput.
type priorArtifactsStub struct {
	role     string
	received chan map[string][]byte
}

func (h *priorArtifactsStub) Role() string { return h.role }

func (h *priorArtifactsStub) Handle(_ context.Context, in StageInput) (StageResult, error) {
	h.received <- in.PriorArtifacts
	return StageResult{Stage: in.Stage, ArtifactJSON: []byte(`{}`)}, nil
}

func TestPoolThreadsPriorArtifactsIntoStageInput(t *testing.T) {
	ctx := context.Background()
	broker := startRedpanda(t)
	createTopics(t, broker, kafka.TopicStageRequested, kafka.TopicStageCompleted, kafka.TopicStageFailed)
	fx := newPool(t)
	prod, _ := kafka.NewProducer([]string{broker})
	defer prod.Close()

	stub := &priorArtifactsStub{role: "qa", received: make(chan map[string][]byte, 1)}

	consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers: []string{broker}, Group: "worker-prior", Topics: []string{kafka.TopicStageRequested},
	})
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	defer consumer.Close()

	p := NewPool(consumer, prod, fx.checkpoints, []StageHandler{stub})
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = p.Run(runCtx) }()

	env, err := events.NewEnvelope("run-prior", "stage.requested", "test", 1)
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{
		"role": "qa", "stage": "qa", "spec": "spec", "repo_ids": []string{"repo-a"},
		"prior_artifacts": map[string]any{"developing": map[string]any{"tests_passed": true}},
	})
	value, err := events.Marshal(&env, payload)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if err := prod.Publish(ctx, kafka.TopicStageRequested, "run-prior", value); err != nil {
		t.Fatalf("publish stage request: %v", err)
	}

	select {
	case got := <-stub.received:
		dev, ok := got["developing"]
		if !ok || !strings.Contains(string(dev), "tests_passed") {
			t.Fatalf("prior_artifacts[developing] = %s, want it to contain tests_passed", dev)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("handler was not invoked with prior_artifacts")
	}

	cancel()
	wg.Wait()
}

// countingStub is a minimal StageHandler that atomically counts invocations,
// so a test can assert whether the pool re-ran the handler or short-circuited
// on a checkpoint.
type countingStub struct {
	role  string
	count *atomic.Int64
}

func (h *countingStub) Role() string { return h.role }

func (h *countingStub) Handle(_ context.Context, in StageInput) (StageResult, error) {
	h.count.Add(1)
	return StageResult{Stage: in.Stage, ArtifactJSON: []byte(`{}`)}, nil
}

func publishStageRequestWithAttempt(t *testing.T, p *kafka.Producer, runID, role, stage, spec string, attempt int) {
	t.Helper()
	env, err := events.NewEnvelope(runID, "stage.requested", "test", 1)
	if err != nil {
		t.Fatalf("envelope: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{
		"role": role, "stage": stage, "spec": spec, "repo_ids": []string{"repo-a"}, "attempt": attempt,
	})
	value, err := events.Marshal(&env, payload)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	if err := p.Publish(context.Background(), kafka.TopicStageRequested, runID, value); err != nil {
		t.Fatalf("publish stage request: %v", err)
	}
}

func TestPoolAttemptReRunsHandler(t *testing.T) {
	ctx := context.Background()
	broker := startRedpanda(t)
	createTopics(t, broker, kafka.TopicStageRequested, kafka.TopicStageCompleted, kafka.TopicStageFailed)
	fx := newPool(t)
	prod, _ := kafka.NewProducer([]string{broker})
	defer prod.Close()

	var count atomic.Int64
	stub := &countingStub{role: "developer", count: &count}

	consumer, err := kafka.NewConsumer(kafka.ConsumerConfig{
		Brokers: []string{broker}, Group: "worker-attempt", Topics: []string{kafka.TopicStageRequested},
	})
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	defer consumer.Close()

	p := NewPool(consumer, prod, fx.checkpoints, []StageHandler{stub})
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = p.Run(runCtx) }()

	// 1) attempt=1 -> handler runs (count 1).
	publishStageRequestWithAttempt(t, prod, "run-attempt", "developer", "developing", "add a feature", 1)
	first := readOne(t, broker, kafka.TopicStageCompleted)
	if first == nil {
		t.Fatal("no completed event for attempt 1")
	}
	if got := count.Load(); got != 1 {
		t.Fatalf("count after attempt 1 = %d, want 1", got)
	}

	// 2) attempt=1 again -> short-circuits on the attempt-1 checkpoint (count stays 1).
	publishStageRequestWithAttempt(t, prod, "run-attempt", "developer", "developing", "add a feature", 1)
	recs := readN(t, broker, kafka.TopicStageCompleted, 2)
	if len(recs) != 2 {
		t.Fatalf("got %d completed records, want 2", len(recs))
	}
	if got := count.Load(); got != 1 {
		t.Fatalf("count after duplicate attempt 1 = %d, want 1 (should short-circuit)", got)
	}

	// 3) attempt=2 -> fresh checkpoint key -> handler runs again (count 2).
	publishStageRequestWithAttempt(t, prod, "run-attempt", "developer", "developing", "add a feature", 2)
	recs = readN(t, broker, kafka.TopicStageCompleted, 3)
	if len(recs) != 3 {
		t.Fatalf("got %d completed records, want 3", len(recs))
	}
	if got := count.Load(); got != 2 {
		t.Fatalf("count after attempt 2 = %d, want 2 (fresh checkpoint key should re-run handler)", got)
	}

	cancel()
	wg.Wait()

	// Both attempt checkpoints should exist independently.
	if _, ok, _ := fx.checkpoints.Load(ctx, "run-attempt", "developing", 1); !ok {
		t.Fatal("expected a checkpoint for attempt 1")
	}
	if _, ok, _ := fx.checkpoints.Load(ctx, "run-attempt", "developing", 2); !ok {
		t.Fatal("expected a checkpoint for attempt 2")
	}
}

// newPool provides a shared CheckpointStore over a fresh Postgres (reuses the
// checkpoint test's newPool DB helper).
type poolFixture struct{ checkpoints *CheckpointStore }

func newPool(t *testing.T) poolFixture {
	s, err := NewCheckpointStore(context.Background(), newPool_pg(t))
	if err != nil {
		t.Fatalf("checkpoint store: %v", err)
	}
	return poolFixture{checkpoints: s}
}
