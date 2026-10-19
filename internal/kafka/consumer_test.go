package kafka

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fin/multi-agent-rag/internal/events"
	"github.com/twmb/franz-go/pkg/kgo"
)

// consumeOne reads a single record from topic starting at offset 0.
func consumeOne(t *testing.T, broker, topic string, timeout time.Duration) *kgo.Record {
	t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(broker),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
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

func TestConsumerHandlesAndCommits(t *testing.T) {
	ctx := context.Background()
	broker := startRedpanda(t)
	topic := "work.topic"
	createTopics(t, broker, topic) // topics are provisioned explicitly (producer no longer auto-creates)

	p, _ := NewProducer([]string{broker})
	defer p.Close()
	if err := p.Publish(ctx, topic, "run-1", []byte("job")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	c, err := NewConsumer(ConsumerConfig{
		Brokers: []string{broker}, Group: "grp-1", Topics: []string{topic},
	})
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	defer c.Close()

	var got atomic.Value
	handled := make(chan struct{})
	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = c.Run(runCtx, func(_ context.Context, rec Record) error {
			got.Store(string(rec.Value))
			close(handled)
			return nil
		})
	}()

	select {
	case <-handled:
	case <-time.After(20 * time.Second):
		t.Fatal("handler never invoked")
	}
	cancel()
	wg.Wait()

	if got.Load() != "job" {
		t.Fatalf("handler saw %v, want job", got.Load())
	}
}

// TestConsumerBatchConcurrency verifies that with Concurrency>1 the records of
// a single poll batch are handled in parallel: each handler blocks until every
// record has arrived, which can only complete if they run concurrently. A
// serial consumer would deadlock here (the barrier would never fill), so the
// test would time out — making concurrency the thing under test.
func TestConsumerBatchConcurrency(t *testing.T) {
	ctx := context.Background()
	broker := startRedpanda(t)
	topic := "concurrent.topic"
	createTopics(t, broker, topic)

	const n = 3
	p, _ := NewProducer([]string{broker})
	defer p.Close()
	for i := 0; i < n; i++ {
		if err := p.Publish(ctx, topic, "run-"+string(rune('a'+i)), []byte("job")); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	c, err := NewConsumer(ConsumerConfig{
		Brokers: []string{broker}, Group: "grp-conc", Topics: []string{topic},
		Concurrency: n,
	})
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	defer c.Close()

	arrived := make(chan struct{}, n) // each handler signals arrival
	release := make(chan struct{})    // closed once all n have arrived
	var maxInFlight int32
	var inFlight int32

	runCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = c.Run(runCtx, func(_ context.Context, _ Record) error {
			cur := atomic.AddInt32(&inFlight, 1)
			for {
				m := atomic.LoadInt32(&maxInFlight)
				if cur <= m || atomic.CompareAndSwapInt32(&maxInFlight, m, cur) {
					break
				}
			}
			arrived <- struct{}{}
			<-release // block until all handlers are concurrently in flight
			atomic.AddInt32(&inFlight, -1)
			return nil
		})
	}()

	// Wait for all n handlers to be simultaneously in flight (proves parallelism).
	for i := 0; i < n; i++ {
		select {
		case <-arrived:
		case <-time.After(25 * time.Second):
			t.Fatalf("only %d/%d handlers ran concurrently — dispatch is serial", i, n)
		}
	}
	close(release)
	cancel()
	wg.Wait()

	if got := atomic.LoadInt32(&maxInFlight); got != n {
		t.Fatalf("max concurrent handlers = %d, want %d", got, n)
	}
}

func TestConsumerRoutesToDLQAfterRetries(t *testing.T) {
	ctx := context.Background()
	broker := startRedpanda(t)
	topic := "fail.topic"
	// Provision both the work topic and its DLQ explicitly (the consumer's DLQ
	// producer no longer auto-creates topics).
	createTopics(t, broker, topic, DLQTopic(topic))

	p, _ := NewProducer([]string{broker})
	defer p.Close()
	if err := p.Publish(ctx, topic, "run-2", []byte("bad-job")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	c, err := NewConsumer(ConsumerConfig{
		Brokers: []string{broker}, Group: "grp-2", Topics: []string{topic},
		MaxAttempts: 3, BaseBackoff: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	defer c.Close()

	var attempts int32
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = c.Run(runCtx, func(_ context.Context, _ Record) error {
			atomic.AddInt32(&attempts, 1)
			return errContext("always fails")
		})
	}()

	// The record should land in the DLQ after MaxAttempts.
	dlq := consumeOne(t, broker, DLQTopic(topic), 25*time.Second)
	if dlq == nil {
		t.Fatal("no record in DLQ")
	}
	if string(dlq.Value) != "bad-job" {
		t.Fatalf("DLQ value = %q, want bad-job", dlq.Value)
	}
	// Error metadata added as headers.
	if headerValue(dlq, "dlq-error") == "" || headerValue(dlq, "dlq-origin-topic") != topic {
		t.Fatalf("DLQ headers missing/incorrect: %+v", dlq.Headers)
	}
	if got := headerValue(dlq, "dlq-attempts"); got != "3" {
		t.Fatalf("DLQ dlq-attempts = %q, want 3", got)
	}
	cancel()
	wg.Wait()

	if n := atomic.LoadInt32(&attempts); n != 3 {
		t.Fatalf("handler attempted %d times, want 3", n)
	}
}

func TestConsumerPoisonSkipsRetry(t *testing.T) {
	ctx := context.Background()
	broker := startRedpanda(t)
	topic := "poison.topic"
	createTopics(t, broker, topic, DLQTopic(topic))

	p, _ := NewProducer([]string{broker})
	defer p.Close()
	if err := p.Publish(ctx, topic, "run-3", []byte("poison-job")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	c, err := NewConsumer(ConsumerConfig{
		Brokers: []string{broker}, Group: "grp-3", Topics: []string{topic},
		MaxAttempts: 3, BaseBackoff: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	defer c.Close()

	var attempts int32
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = c.Run(runCtx, func(_ context.Context, _ Record) error {
			atomic.AddInt32(&attempts, 1)
			return events.Poison(errContext("non-retryable"))
		})
	}()

	// The record should land in the DLQ immediately, skipping retry.
	dlq := consumeOne(t, broker, DLQTopic(topic), 25*time.Second)
	if dlq == nil {
		t.Fatal("no record in DLQ")
	}
	if string(dlq.Value) != "poison-job" {
		t.Fatalf("DLQ value = %q, want poison-job", dlq.Value)
	}
	if headerValue(dlq, "dlq-error") == "" || headerValue(dlq, "dlq-origin-topic") != topic {
		t.Fatalf("DLQ headers missing/incorrect: %+v", dlq.Headers)
	}
	if got := headerValue(dlq, "dlq-attempts"); got != "1" {
		t.Fatalf("DLQ dlq-attempts = %q, want 1", got)
	}
	cancel()
	wg.Wait()

	if n := atomic.LoadInt32(&attempts); n != 1 {
		t.Fatalf("handler attempted %d times, want 1 (poison should skip retry)", n)
	}
}

func errContext(s string) error { return &simpleErr{s} }

type simpleErr struct{ s string }

func (e *simpleErr) Error() string { return e.s }

func headerValue(rec *kgo.Record, key string) string {
	for _, h := range rec.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}
