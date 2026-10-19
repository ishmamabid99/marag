package gateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/fin/multi-agent-rag/internal/events"
	"github.com/fin/multi-agent-rag/internal/kafka"
)

type fakeProducer struct{ recs []rec }
type rec struct {
	topic, key string
	value      []byte
}

func (f *fakeProducer) Publish(_ context.Context, topic, key string, value []byte) error {
	f.recs = append(f.recs, rec{topic, key, value})
	return nil
}
func (f *fakeProducer) last() rec { return f.recs[len(f.recs)-1] }

func newGW(fp *fakeProducer) *Gateway {
	g := New(fp, nil, nil, nil)
	n := 0
	return g.WithIDFunc(func() string { n++; return "run-fixed" })
}

func TestSubmitRunProducesRunRequested(t *testing.T) {
	fp := &fakeProducer{}
	g := newGW(fp)
	id, err := g.SubmitRun(context.Background(), "add currency", []string{"payments"})
	if err != nil {
		t.Fatalf("SubmitRun: %v", err)
	}
	if id != "run-fixed" {
		t.Fatalf("id = %q", id)
	}
	r := fp.last()
	if r.topic != kafka.TopicRunRequested || r.key != "run-fixed" {
		t.Fatalf("rec = %+v", r)
	}
	env, payload, err := events.Unmarshal(r.value)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Type != "run.requested" || env.RunId != "run-fixed" {
		t.Fatalf("env = %+v", env)
	}
	var p struct {
		Spec    string   `json:"spec"`
		RepoIDs []string `json:"repo_ids"`
	}
	_ = json.Unmarshal(payload, &p)
	if p.Spec != "add currency" || len(p.RepoIDs) != 1 || p.RepoIDs[0] != "payments" {
		t.Fatalf("payload = %+v", p)
	}
}

func TestSubmitRunRejectsEmptySpec(t *testing.T) {
	fp := &fakeProducer{}
	if _, err := newGW(fp).SubmitRun(context.Background(), "   ", nil); err == nil {
		t.Fatalf("expected ErrEmptySpec")
	}
	if len(fp.recs) != 0 {
		t.Fatalf("should not have produced on empty spec")
	}
}

func TestSubmitRunKeyedIsDeterministic(t *testing.T) {
	fp := &fakeProducer{}
	g := New(fp, nil, nil, nil) // real randomID; keyed path must ignore it
	id1, err := g.SubmitRunKeyed(context.Background(), "linear-comment-42", "add feature", []string{"r"})
	if err != nil {
		t.Fatalf("SubmitRunKeyed: %v", err)
	}
	id2, err := g.SubmitRunKeyed(context.Background(), "linear-comment-42", "add feature", []string{"r"})
	if err != nil {
		t.Fatalf("SubmitRunKeyed: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("keyed run ids differ: %q vs %q", id1, id2)
	}
	if id1 == "" || id1[:4] != "run-" {
		t.Fatalf("id = %q", id1)
	}
	// A different key → a different id.
	id3, _ := g.SubmitRunKeyed(context.Background(), "linear-comment-99", "add feature", []string{"r"})
	if id3 == id1 {
		t.Fatalf("different keys produced same id")
	}
	// Empty key → random (delegated), still valid.
	id4, _ := g.SubmitRunKeyed(context.Background(), "", "add feature", nil)
	if id4 == "" || id4 == id1 {
		t.Fatalf("empty-key id = %q", id4)
	}
}

func TestResumeGateProducesGateResume(t *testing.T) {
	fp := &fakeProducer{}
	g := newGW(fp)
	if err := g.ResumeGate(context.Background(), "run-9", true); err != nil {
		t.Fatalf("ResumeGate: %v", err)
	}
	r := fp.last()
	if r.topic != kafka.TopicGateResume || r.key != "run-9" {
		t.Fatalf("rec = %+v", r)
	}
	env, payload, _ := events.Unmarshal(r.value)
	if env.Type != "gate.resume" {
		t.Fatalf("type = %s", env.Type)
	}
	var p struct {
		Approved bool `json:"approved"`
	}
	_ = json.Unmarshal(payload, &p)
	if !p.Approved {
		t.Fatalf("approved = false, want true")
	}
}
