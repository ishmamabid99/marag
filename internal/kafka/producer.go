// Package kafka wraps franz-go with the platform's producer, consumer group,
// retry, and dead-letter conventions.
package kafka

import (
	"context"
	"fmt"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Producer publishes records synchronously (waits for broker ack).
type Producer struct {
	client *kgo.Client
}

// ProducerOption configures a Producer at construction.
type ProducerOption func(*producerOpts)

type producerOpts struct{ auth ClientAuth }

// WithAuth applies SASL/TLS/compression/retry settings to the producer.
func WithAuth(a ClientAuth) ProducerOption {
	return func(o *producerOpts) { o.auth = a }
}

// NewProducer connects to the given seed brokers. Pass WithAuth for SASL/TLS.
func NewProducer(brokers []string, options ...ProducerOption) (*Producer, error) {
	var po producerOpts
	for _, o := range options {
		o(&po)
	}
	authOpts, err := po.auth.Opts()
	if err != nil {
		return nil, fmt.Errorf("new producer auth: %w", err)
	}
	opts := append([]kgo.Opt{kgo.SeedBrokers(brokers...)}, authOpts...)
	cl, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("new producer client: %w", err)
	}
	return &Producer{client: cl}, nil
}

// Publish produces one record and returns only after the broker acknowledges.
func (p *Producer) Publish(ctx context.Context, topic, key string, value []byte) error {
	rec := &kgo.Record{Topic: topic, Key: []byte(key), Value: value}
	if err := p.client.ProduceSync(ctx, rec).FirstErr(); err != nil {
		return fmt.Errorf("publish to %s: %w", topic, err)
	}
	return nil
}

// Close closes the underlying client.
func (p *Producer) Close() { p.client.Close() }
