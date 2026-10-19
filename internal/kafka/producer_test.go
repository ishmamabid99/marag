package kafka

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/redpanda"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
)

// startRedpanda spins up a single-broker Redpanda and returns its seed broker.
// Shared by producer and consumer tests.
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

// createTopics explicitly provisions topics for tests. Production code paths
// never auto-create topics (see NewProducer), so tests that need a topic to
// exist must create it themselves.
func createTopics(t *testing.T, broker string, topics ...string) {
	t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(broker))
	if err != nil {
		t.Fatalf("admin client: %v", err)
	}
	defer cl.Close()
	adm := kadm.NewClient(cl)
	resp, err := adm.CreateTopics(context.Background(), 1, 1, nil, topics...)
	if err != nil {
		t.Fatalf("create topics: %v", err)
	}
	for _, tr := range resp.Sorted() {
		// Tolerate "already exists"; fail on anything else.
		if tr.Err != nil && !errors.Is(tr.Err, kerr.TopicAlreadyExists) {
			t.Fatalf("create topic %s: %v", tr.Topic, tr.Err)
		}
	}
}

func TestProducerPublish(t *testing.T) {
	ctx := context.Background()
	broker := startRedpanda(t)

	p, err := NewProducer([]string{broker})
	if err != nil {
		t.Fatalf("NewProducer: %v", err)
	}
	defer p.Close()

	createTopics(t, broker, "test.topic")

	if err := p.Publish(ctx, "test.topic", "run-1", []byte("payload-1")); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Read it back with a raw franz-go consumer to prove it landed.
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(broker),
		kgo.ConsumeTopics("test.topic"),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatalf("reader client: %v", err)
	}
	defer cl.Close()

	pollCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	fetches := cl.PollFetches(pollCtx)
	if err := fetches.Err(); err != nil {
		t.Fatalf("poll: %v", err)
	}
	recs := fetches.Records()
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1", len(recs))
	}
	if string(recs[0].Key) != "run-1" || string(recs[0].Value) != "payload-1" {
		t.Fatalf("record = key %q value %q", recs[0].Key, recs[0].Value)
	}
}
