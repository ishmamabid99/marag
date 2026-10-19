package linear

import (
	"context"
	"strings"
	"testing"
)

func TestNotifyPostsPRLinks(t *testing.T) {
	store := NewFakeRunStore()
	_ = store.SaveMapping(context.Background(), "run-1", "iss-5")
	client := &FakeClient{}
	n := NewNotifier(store, client)
	err := n.Notify(context.Background(), "run-1",
		[]OpenedPR{{RepoID: "payments", URL: "https://github.com/x/payments/pull/1"}},
		[]FailedRepo{{RepoID: "checkout", Error: "qa failed"}})
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(client.Comments) != 1 || client.Comments[0].IssueID != "iss-5" {
		t.Fatalf("comments = %+v", client.Comments)
	}
	body := client.Comments[0].Body
	if !strings.Contains(body, "pull/1") || !strings.Contains(body, "checkout") {
		t.Fatalf("comment body missing PR link or failure: %q", body)
	}
}

func TestNotifyUnknownRunNoop(t *testing.T) {
	n := NewNotifier(NewFakeRunStore(), &FakeClient{})
	if err := n.Notify(context.Background(), "unknown", nil, nil); err != nil {
		t.Fatalf("unknown run should be a no-op, got %v", err)
	}
}

func TestNotifyGatePostsProposal(t *testing.T) {
	store := NewFakeRunStore()
	_ = store.SaveMapping(context.Background(), "run-1", "iss-9")
	client := &FakeClient{}
	n := NewNotifier(store, client)
	if err := n.NotifyGate(context.Background(), "run-1", "analyzing",
		"proposed additional repos: bookstore — approve to include"); err != nil {
		t.Fatalf("NotifyGate: %v", err)
	}
	if len(client.Comments) != 1 || client.Comments[0].IssueID != "iss-9" {
		t.Fatalf("comments = %+v", client.Comments)
	}
	body := client.Comments[0].Body
	if !strings.Contains(body, "bookstore") || !strings.Contains(strings.ToLower(body), "approval") {
		t.Fatalf("gate comment body missing proposal/approval: %q", body)
	}
}

func TestNotifyGateUnknownRunNoop(t *testing.T) {
	n := NewNotifier(NewFakeRunStore(), &FakeClient{})
	if err := n.NotifyGate(context.Background(), "unknown", "analyzing", "x"); err != nil {
		t.Fatalf("unknown run should be a no-op, got %v", err)
	}
}

type fakeArtifacts struct{ m map[string][]byte } // key: runID|stage

func (f fakeArtifacts) Artifact(_ context.Context, runID, stage string) ([]byte, bool, error) {
	v, ok := f.m[runID+"|"+stage]
	return v, ok, nil
}

func TestNotifyGateEnrichedFromArtifact(t *testing.T) {
	store := NewFakeRunStore()
	_ = store.SaveMapping(context.Background(), "run-1", "iss-1")
	arts := fakeArtifacts{m: map[string][]byte{
		"run-1|risk": []byte(`{"gate_reason":"public metrics endpoint could expose internal data","blast_radius":"medium","breaking_risks":["exposes internal metrics","global middleware latency"]}`),
	}}
	client := &FakeClient{}
	n := NewNotifier(store, client).WithArtifacts(arts)
	if err := n.NotifyGate(context.Background(), "run-1", "risk", "generic fallback"); err != nil {
		t.Fatalf("NotifyGate: %v", err)
	}
	body := client.Comments[0].Body
	for _, want := range []string{"public metrics endpoint", "medium", "exposes internal metrics", "global middleware latency", "approve", "reject"} {
		if !strings.Contains(body, want) {
			t.Fatalf("enriched gate body missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "generic fallback") {
		t.Fatalf("artifact gate_reason should override the generic reason:\n%s", body)
	}
}

func TestNotifyFailurePostsReason(t *testing.T) {
	store := NewFakeRunStore()
	_ = store.SaveMapping(context.Background(), "run-1", "iss-1")
	client := &FakeClient{}
	n := NewNotifier(store, client)
	if err := n.NotifyFailure(context.Background(), "run-1", "developing",
		"developer: deterministic QA not green after 3 attempts:\ncontroller.go:10: undefined: promhttp"); err != nil {
		t.Fatalf("NotifyFailure: %v", err)
	}
	body := client.Comments[0].Body
	for _, want := range []string{"failed", "developing", "undefined: promhttp"} {
		if !strings.Contains(body, want) {
			t.Fatalf("failure body missing %q:\n%s", want, body)
		}
	}
}

func TestNotifyFailureUnknownRunNoop(t *testing.T) {
	n := NewNotifier(NewFakeRunStore(), &FakeClient{})
	if err := n.NotifyFailure(context.Background(), "unknown", "developing", "x"); err != nil {
		t.Fatalf("unknown run should be a no-op, got %v", err)
	}
}
