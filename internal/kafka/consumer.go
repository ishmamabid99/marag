package kafka

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/fin/multi-agent-rag/internal/events"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Record is a consumed Kafka record handed to a Handler.
type Record struct {
	Topic string
	Key   string
	Value []byte
}

// Handler processes one record. Returning nil commits it; returning an error
// triggers bounded retry then DLQ. Return events.Poison(err) to skip retry.
type Handler func(ctx context.Context, rec Record) error

// ConsumerConfig configures a consumer group.
type ConsumerConfig struct {
	Brokers     []string
	Group       string
	Topics      []string
	MaxAttempts int           // default 3
	BaseBackoff time.Duration // default 100ms
	Auth        ClientAuth    // zero value ⇒ plaintext
	// Concurrency bounds how many records from a single poll batch are handled
	// in parallel (default 1 ⇒ serial, backward-compatible). Values >1 dispatch
	// the batch to a bounded worker set and commit offsets only after the whole
	// batch drains — preserving at-least-once semantics while removing the
	// head-of-line blocking a slow handler otherwise imposes on its batch-mates.
	Concurrency int
}

// Consumer is a franz-go consumer group with manual commit, bounded retry, and
// dead-letter routing.
type Consumer struct {
	client      *kgo.Client
	producer    *Producer
	maxAttempts int
	baseBackoff time.Duration
	concurrency int
}

// NewConsumer builds a consumer group with auto-commit disabled.
func NewConsumer(cfg ConsumerConfig) (*Consumer, error) {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = 100 * time.Millisecond
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 1
	}
	authOpts, err := cfg.Auth.Opts()
	if err != nil {
		return nil, fmt.Errorf("new consumer auth: %w", err)
	}
	clientOpts := append([]kgo.Opt{
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ConsumerGroup(cfg.Group),
		kgo.ConsumeTopics(cfg.Topics...),
		kgo.DisableAutoCommit(),
	}, authOpts...)
	cl, err := kgo.NewClient(clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("new consumer client: %w", err)
	}
	prod, err := NewProducer(cfg.Brokers, WithAuth(cfg.Auth))
	if err != nil {
		cl.Close()
		return nil, fmt.Errorf("new dlq producer: %w", err)
	}
	return &Consumer{client: cl, producer: prod, maxAttempts: cfg.MaxAttempts, baseBackoff: cfg.BaseBackoff, concurrency: cfg.Concurrency}, nil
}

// Run polls and handles records until ctx is cancelled. Each record is handled
// with bounded backoff retry; on exhaustion or a poison error it is routed to
// the DLQ. Offsets are committed only after handle-or-DLQ. Run returns nil on
// clean cancellation, and a wrapped error on an unrecoverable condition
// (poll-fetch failure, offset-commit failure, or a DLQ-publish failure).
func (c *Consumer) Run(ctx context.Context, h Handler) error {
	// Global in-flight limiter shared across poll batches. cap==1 ⇒ serial.
	sem := make(chan struct{}, c.concurrency)
	for {
		fetches := c.client.PollFetches(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if errs := fetches.Errors(); len(errs) > 0 {
			// Fetch errors (not handler errors) — surface the first.
			return fmt.Errorf("poll fetches: %w", errs[0].Err)
		}

		var (
			wg       sync.WaitGroup
			mu       sync.Mutex
			handled  []*kgo.Record
			firstErr error
		)
		iter := fetches.RecordIter()
	dispatch:
		for !iter.Done() {
			rec := iter.Next()
			// Acquire an in-flight slot; stop dispatching on shutdown and drain
			// whatever is already in flight below.
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				break dispatch
			}
			wg.Add(1)
			go func(rec *kgo.Record) {
				defer wg.Done()
				defer func() { <-sem }()
				err := c.process(ctx, rec, h) // handles retry/DLQ; err only on fatal DLQ-publish failure
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					if firstErr == nil {
						firstErr = err
					}
					return
				}
				handled = append(handled, rec)
			}(rec)
		}
		wg.Wait()

		if firstErr != nil {
			return firstErr // fatal (DLQ-publish failure) — do not commit
		}
		if ctx.Err() != nil {
			return nil // shutting down: don't commit a partial batch with a dead context
		}
		// firstErr==nil ⇒ every dispatched record was handled-or-DLQ'd, so the
		// whole batch is safe to commit together.
		if len(handled) > 0 {
			if err := c.client.CommitRecords(ctx, handled...); err != nil {
				return fmt.Errorf("commit: %w", err)
			}
		}
	}
}

// process handles one record with retry/DLQ. It returns an error only if the
// DLQ publish itself fails (a fatal, unrecoverable condition).
func (c *Consumer) process(ctx context.Context, rec *kgo.Record, h Handler) error {
	r := Record{Topic: rec.Topic, Key: string(rec.Key), Value: rec.Value}
	var lastErr error
	attempts := 0
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		attempts = attempt
		lastErr = h(ctx, r)
		if lastErr == nil {
			return nil
		}
		if events.IsPoison(lastErr) {
			break // non-retryable → straight to DLQ
		}
		if attempt < c.maxAttempts {
			select {
			case <-time.After(c.backoff(attempt)):
			case <-ctx.Done():
				return nil
			}
		}
	}
	return c.toDLQ(ctx, rec, lastErr, attempts)
}

func (c *Consumer) backoff(attempt int) time.Duration {
	// exponential: base * 2^(attempt-1)
	d := c.baseBackoff
	for i := 1; i < attempt; i++ {
		d *= 2
	}
	return d
}

func (c *Consumer) toDLQ(ctx context.Context, rec *kgo.Record, cause error, attempts int) error {
	dlqRec := &kgo.Record{
		Topic: DLQTopic(rec.Topic),
		Key:   rec.Key,
		Value: rec.Value,
		Headers: []kgo.RecordHeader{
			{Key: "dlq-origin-topic", Value: []byte(rec.Topic)},
			{Key: "dlq-error", Value: []byte(errString(cause))},
			{Key: "dlq-attempts", Value: []byte(strconv.Itoa(attempts))},
		},
	}
	if err := c.producer.client.ProduceSync(ctx, dlqRec).FirstErr(); err != nil {
		return fmt.Errorf("dlq publish for %s: %w", rec.Topic, err)
	}
	return nil
}

func errString(err error) string {
	if err == nil {
		return "unknown"
	}
	return err.Error()
}

// Close closes the consumer and its DLQ producer.
func (c *Consumer) Close() {
	c.client.Close()
	c.producer.Close()
}
