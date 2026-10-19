// Package events defines the platform event envelope and (un)marshal helpers
// shared by all Kafka producers and consumers.
package events

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/fin/multi-agent-rag/internal/events/eventspb"
	"google.golang.org/protobuf/proto"
)

// NewEnvelope builds an envelope with a fresh event_id, current timestamp, and
// a deterministic idempotency key (run_id:type:attempt).
func NewEnvelope(runID, eventType, producer string, attempt int) (eventspb.Envelope, error) {
	id, err := randomID()
	if err != nil {
		return eventspb.Envelope{}, fmt.Errorf("new envelope: %w", err)
	}
	return eventspb.Envelope{
		EventId:        id,
		RunId:          runID,
		Type:           eventType,
		Version:        1,
		OccurredAt:     time.Now().UTC().Format(time.RFC3339),
		Producer:       producer,
		IdempotencyKey: runID + ":" + eventType + ":" + strconv.Itoa(attempt),
	}, nil
}

// Marshal encodes an envelope + payload into the Event wire form.
func Marshal(env *eventspb.Envelope, payload []byte) ([]byte, error) {
	b, err := proto.Marshal(&eventspb.Event{Envelope: env, Payload: payload})
	if err != nil {
		return nil, fmt.Errorf("marshal event: %w", err)
	}
	return b, nil
}

// Unmarshal decodes an Event wire value into its envelope and payload.
func Unmarshal(b []byte) (*eventspb.Envelope, []byte, error) {
	var e eventspb.Event
	if err := proto.Unmarshal(b, &e); err != nil {
		return nil, nil, fmt.Errorf("unmarshal event: %w", err)
	}
	return e.Envelope, e.Payload, nil
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("random id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// PoisonError marks an error as non-retryable: the consumer routes it straight
// to the DLQ without retrying.
type PoisonError struct{ Err error }

func (p *PoisonError) Error() string { return "poison: " + p.Err.Error() }
func (p *PoisonError) Unwrap() error { return p.Err }

// Poison wraps err as non-retryable.
func Poison(err error) error { return &PoisonError{Err: err} }

// IsPoison reports whether err (or anything it wraps) is a PoisonError.
func IsPoison(err error) bool {
	var p *PoisonError
	return errors.As(err, &p)
}
