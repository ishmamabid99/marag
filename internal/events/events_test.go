package events

import (
	"errors"
	"strings"
	"testing"
)

func TestNewEnvelope(t *testing.T) {
	env, err := NewEnvelope("run-123", "stage.completed.developer", "worker", 2)
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	if env.RunId != "run-123" || env.Type != "stage.completed.developer" || env.Producer != "worker" {
		t.Fatalf("fields = %+v", &env)
	}
	if env.Version != 1 {
		t.Fatalf("version = %d, want 1", env.Version)
	}
	if env.EventId == "" || env.OccurredAt == "" {
		t.Fatalf("event_id/occurred_at not set: %+v", &env)
	}
	if env.IdempotencyKey != "run-123:stage.completed.developer:2" {
		t.Fatalf("idempotency_key = %q", env.IdempotencyKey)
	}
	// event_id is unique per call.
	env2, _ := NewEnvelope("run-123", "stage.completed.developer", "worker", 2)
	if env2.EventId == env.EventId {
		t.Fatal("event_id should be unique per envelope")
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	env, _ := NewEnvelope("run-1", "spec.received", "trigger", 1)
	payload := []byte("hello-artifact")
	b, err := Marshal(&env, payload)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	gotEnv, gotPayload, err := Unmarshal(b)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if gotEnv.RunId != "run-1" || gotEnv.Type != "spec.received" {
		t.Fatalf("envelope round-trip lost fields: %+v", gotEnv)
	}
	if string(gotPayload) != "hello-artifact" {
		t.Fatalf("payload = %q", gotPayload)
	}
}

func TestPoisonError(t *testing.T) {
	base := errors.New("bad schema")
	p := Poison(base)
	if !IsPoison(p) {
		t.Fatal("IsPoison should be true for a poison error")
	}
	if !errors.Is(p, base) {
		t.Fatal("poison error should unwrap to the base error")
	}
	if IsPoison(base) {
		t.Fatal("plain error should not be poison")
	}
	if !strings.Contains(p.Error(), "bad schema") {
		t.Fatalf("poison Error() = %q", p.Error())
	}
}
