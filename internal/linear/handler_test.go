package linear

import (
	"context"
	"strings"
	"testing"
)

type fakeSubmitter struct {
	spec    string
	repoIDs []string
	key     string
	id      string
}

func (f *fakeSubmitter) SubmitRunKeyed(_ context.Context, key, spec string, repoIDs []string) (string, error) {
	f.key, f.spec, f.repoIDs = key, spec, repoIDs
	if f.id == "" {
		f.id = "run-xyz"
	}
	return f.id, nil
}

func TestHandlerTriggersAndAcks(t *testing.T) {
	body := []byte(`{"action":"create","type":"Comment","data":{"id":"cmt-1","body":"@marag implement it","issue":{"id":"iss-1","title":"Add EUR","description":"Support EUR. Repos: payments, checkout"}}}`)
	sig := sign(body, "sec")
	sub := &fakeSubmitter{}
	fc := &FakeClient{}
	h := NewHandler("sec", "@marag", sub, fc)

	triggered, runID, err := h.Handle(context.Background(), sig, body)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !triggered || runID != "run-xyz" {
		t.Fatalf("triggered=%v runID=%q", triggered, runID)
	}
	// Spec built from the issue; repos parsed from "Repos:" hint.
	if sub.spec == "" || len(sub.repoIDs) != 2 || sub.repoIDs[0] != "payments" {
		t.Fatalf("submit spec=%q repos=%v", sub.spec, sub.repoIDs)
	}
	// The deterministic key is the event's own SourceID (the comment id).
	if sub.key != "cmt-1" {
		t.Fatalf("submit key = %q, want cmt-1", sub.key)
	}
	// Acknowledged on the issue with the run id.
	if len(fc.Comments) != 1 || fc.Comments[0].IssueID != "iss-1" {
		t.Fatalf("comments = %+v", fc.Comments)
	}
}

func TestHandlerNoTriggerToken(t *testing.T) {
	body := []byte(`{"action":"create","type":"Comment","data":{"body":"just a normal comment","issue":{"id":"i","title":"T","description":"D"}}}`)
	sub := &fakeSubmitter{}
	h := NewHandler("sec", "@marag", sub, &FakeClient{})
	triggered, _, err := h.Handle(context.Background(), sign(body, "sec"), body)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if triggered {
		t.Fatalf("should not trigger without the token")
	}
	if sub.spec != "" {
		t.Fatalf("should not have submitted")
	}
}

func TestHandlerIgnoresNonCreate(t *testing.T) {
	body := []byte(`{"action":"update","type":"Comment","data":{"body":"@marag do it","issue":{"id":"i","title":"T","description":"D"}}}`)
	sub := &fakeSubmitter{}
	fc := &FakeClient{}
	h := NewHandler("sec", "@marag", sub, fc)

	triggered, _, err := h.Handle(context.Background(), sign(body, "sec"), body)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if triggered {
		t.Fatalf("update action must not trigger a run")
	}
	if sub.spec != "" {
		t.Fatalf("must not have submitted a run")
	}
	if len(fc.Comments) != 0 {
		t.Fatalf("must not have posted a comment")
	}
}

func TestHandlerSavesRunMapping(t *testing.T) {
	body := []byte(`{"action":"create","type":"Comment","data":{"id":"cmt-1","body":"@marag go","issue":{"id":"iss-7","title":"T","description":"D"}}}`)
	sub := &fakeSubmitter{id: "run-abc"}
	store := NewFakeRunStore()
	h := NewHandler("sec", "@marag", sub, &FakeClient{}).WithRunStore(store)
	if _, _, err := h.Handle(context.Background(), sign(body, "sec"), body); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	issue, ok, _ := store.IssueForRun(context.Background(), "run-abc")
	if !ok || issue != "iss-7" {
		t.Fatalf("mapping = %q,%v", issue, ok)
	}
}

func TestHandlerRejectsBadSignature(t *testing.T) {
	body := []byte(`{"type":"Issue","data":{"id":"i"}}`)
	sub := &fakeSubmitter{}
	h := NewHandler("sec", "@marag", sub, &FakeClient{})
	if _, _, err := h.Handle(context.Background(), "wrong", body); err == nil {
		t.Fatalf("expected signature error")
	}
	if sub.spec != "" {
		t.Fatalf("must not submit on bad signature")
	}
}

type fakeResumer struct {
	called   bool
	runID    string
	approved bool
}

func (f *fakeResumer) ResumeGate(_ context.Context, runID string, approved bool) error {
	f.called, f.runID, f.approved = true, runID, approved
	return nil
}

func TestHandlerApproveResumesGate(t *testing.T) {
	ctx := context.Background()
	store := NewFakeRunStore()
	_ = store.SaveMapping(ctx, "run-abc", "iss-1")
	sub := &fakeSubmitter{}
	res := &fakeResumer{}
	fc := &FakeClient{}
	h := NewHandler("sec", "@marag", sub, fc).WithRunStore(store).WithResumer(res)

	body := []byte(`{"action":"create","type":"Comment","data":{"id":"cmt-2","body":"@marag approve","issue":{"id":"iss-1","title":"t","description":"d"}}}`)
	triggered, runID, err := h.Handle(ctx, sign(body, "sec"), body)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !triggered || runID != "run-abc" {
		t.Fatalf("triggered=%v runID=%q want true/run-abc", triggered, runID)
	}
	if !res.called || res.runID != "run-abc" || !res.approved {
		t.Fatalf("resume=%+v want called run-abc approved=true", res)
	}
	if sub.spec != "" {
		t.Fatalf("approve must NOT submit a new run, got spec=%q", sub.spec)
	}
	if len(fc.Comments) != 1 || !strings.Contains(fc.Comments[0].Body, "Approved") {
		t.Fatalf("ack = %+v", fc.Comments)
	}
}

func TestHandlerRejectResumesGate(t *testing.T) {
	ctx := context.Background()
	store := NewFakeRunStore()
	_ = store.SaveMapping(ctx, "run-r", "iss-9")
	res := &fakeResumer{}
	h := NewHandler("sec", "@marag", &fakeSubmitter{}, &FakeClient{}).WithRunStore(store).WithResumer(res)
	body := []byte(`{"action":"create","type":"Comment","data":{"id":"c","body":"@marag reject","issue":{"id":"iss-9","title":"t","description":"d"}}}`)
	if _, _, err := h.Handle(ctx, sign(body, "sec"), body); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !res.called || res.runID != "run-r" || res.approved {
		t.Fatalf("resume=%+v want called run-r approved=false", res)
	}
}

func TestHandlerNonGateCommentStillSubmits(t *testing.T) {
	ctx := context.Background()
	store := NewFakeRunStore()
	sub := &fakeSubmitter{}
	res := &fakeResumer{}
	h := NewHandler("sec", "@marag", sub, &FakeClient{}).WithRunStore(store).WithResumer(res)
	// No approve/reject keyword → normal submit even with a resumer wired.
	body := []byte(`{"action":"create","type":"Comment","data":{"id":"c","body":"@marag build a thing","issue":{"id":"iss-1","title":"t","description":"d"}}}`)
	if _, _, err := h.Handle(ctx, sign(body, "sec"), body); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if res.called {
		t.Fatal("non-gate comment must not resume")
	}
	if sub.spec == "" {
		t.Fatal("non-gate comment should submit a run")
	}
}

func TestWithAllowedActorsDropsMalformedEmail(t *testing.T) {
	h := NewHandler("sec", "@marag", &fakeSubmitter{}, &FakeClient{}).
		WithAllowedActors([]string{"not-an-email@", "  ", "good@fin.com", "u-123"})
	if !h.allowed["good@fin.com"] || !h.allowed["u-123"] || h.allowed["not-an-email@"] {
		t.Fatalf("allowed set wrong: %v", h.allowed)
	}
}

func TestHandlerActorAllowlistBlocksUnauthorized(t *testing.T) {
	body := []byte(`{"action":"create","type":"Comment","data":{"id":"c1","body":"@marag build it","issue":{"id":"iss-1","title":"t","description":"d"}},"actor":{"id":"u1","name":"Ish","email":"ish@fin.com"}}`)
	sub := &fakeSubmitter{}
	h := NewHandler("sec", "@marag", sub, &FakeClient{}).WithAllowedActors([]string{"boss@fin.com"})
	triggered, _, err := h.Handle(context.Background(), sign(body, "sec"), body)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if triggered || sub.spec != "" {
		t.Fatal("unauthorized actor must be blocked (no trigger, no submit)")
	}
}

func TestHandlerActorAllowlistAllowsByEmailCaseInsensitive(t *testing.T) {
	body := []byte(`{"action":"create","type":"Comment","data":{"id":"c1","body":"@marag build it","issue":{"id":"iss-1","title":"t","description":"d"}},"actor":{"id":"u1","name":"Ish","email":"Ish@Fin.com"}}`)
	sub := &fakeSubmitter{}
	h := NewHandler("sec", "@marag", sub, &FakeClient{}).WithAllowedActors([]string{"ish@fin.com"})
	triggered, _, err := h.Handle(context.Background(), sign(body, "sec"), body)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !triggered || sub.spec == "" {
		t.Fatal("allowlisted actor (case-insensitive email) should trigger")
	}
}

func TestHandlerEmptyAllowlistAllowsAll(t *testing.T) {
	body := []byte(`{"action":"create","type":"Comment","data":{"id":"c1","body":"@marag build it","issue":{"id":"iss-1","title":"t","description":"d"}},"actor":{"id":"u1","email":"anyone@x.com"}}`)
	sub := &fakeSubmitter{}
	h := NewHandler("sec", "@marag", sub, &FakeClient{}) // no allowlist
	triggered, _, err := h.Handle(context.Background(), sign(body, "sec"), body)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !triggered {
		t.Fatal("empty allowlist should allow everyone")
	}
}
