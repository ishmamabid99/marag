package publisher

import (
	"context"
	"strings"
	"testing"

	"github.com/fin/multi-agent-rag/internal/events"
	"github.com/fin/multi-agent-rag/internal/gitprovider"
	"github.com/fin/multi-agent-rag/internal/kafka"
)

// idRepos returns per-id metadata.
type idRepos struct{}

func (idRepos) Get(_ context.Context, id string) (RepoMeta, error) {
	return RepoMeta{Provider: "github", RemoteURL: "https://github.com/acme/" + id, DefaultBranch: "main"}, nil
}

type fakeComp struct {
	runID string
	comps []Comp
}

func (f *fakeComp) Register(_ context.Context, runID string, comps []Comp) error {
	f.runID = runID
	f.comps = append(f.comps, comps...)
	return nil
}

type fakeProd struct {
	recs []struct {
		topic string
		value []byte
	}
}

func (f *fakeProd) Publish(_ context.Context, topic, _ string, value []byte) error {
	f.recs = append(f.recs, struct {
		topic string
		value []byte
	}{topic, value})
	return nil
}

func TestDeliverOpensOnePRPerRepo(t *testing.T) {
	fp := &fakeProd{}
	fc := &fakeComp{}
	prov := gitprovider.NewFake()
	pub := New(idRepos{}, func(string) (gitprovider.GitProvider, error) { return prov, nil },
		func(string) string { return "tok" }, fc, fp)
	opened, err := pub.Deliver(context.Background(), DeliverEvent{
		RunID: "run-1", Title: "T", Body: "B",
		Repos: []DeliverRepo{{RepoID: "repo-a", Branch: "marag/run-1"}, {RepoID: "repo-b", Branch: "marag/run-1"}},
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(opened) != 2 || len(prov.Created) != 2 {
		t.Fatalf("opened=%+v created=%+v", opened, prov.Created)
	}
	// Cross-link mention present in bodies.
	if !strings.Contains(prov.Created[0].Body, "repo-b") {
		t.Fatalf("body missing sibling mention: %q", prov.Created[0].Body)
	}
	// Two compensations per repo → 4 total.
	if len(fc.comps) != 4 {
		t.Fatalf("comps = %d, want 4", len(fc.comps))
	}
	if fc.comps[0].Action != "delete_branch" || fc.comps[1].Action != "close_pr" {
		t.Fatalf("comps order = %+v, want [delete_branch, close_pr, ...]", fc.comps)
	}
	// run.pr.opened emitted listing both PRs.
	env, body, _ := events.Unmarshal(fp.recs[len(fp.recs)-1].value)
	if env.Type != "run.pr.opened" || !strings.Contains(string(body), "repo-a") || !strings.Contains(string(body), "repo-b") {
		t.Fatalf("opened event = %s", body)
	}
	if fp.recs[len(fp.recs)-1].topic != kafka.TopicRunPROpened {
		t.Fatalf("topic = %s", fp.recs[len(fp.recs)-1].topic)
	}
}

func TestDeliverEmptyReposErrors(t *testing.T) {
	pub := New(idRepos{}, func(string) (gitprovider.GitProvider, error) { return gitprovider.NewFake(), nil },
		func(string) string { return "t" }, &fakeComp{}, &fakeProd{})
	if _, err := pub.Deliver(context.Background(), DeliverEvent{RunID: "r", Repos: nil}); err == nil {
		t.Fatalf("expected error for no repos")
	}
}

func TestDeliverPartialFailureBestEffort(t *testing.T) {
	fp := &fakeProd{}
	fc := &fakeComp{}
	prov := gitprovider.NewFake()
	pub := New(idRepos{}, func(string) (gitprovider.GitProvider, error) { return prov, nil },
		func(string) string { return "tok" }, fc, fp)
	// repo-b has an empty branch, so openOne fails for it; repo-a still succeeds.
	opened, err := pub.Deliver(context.Background(), DeliverEvent{
		RunID: "run-2", Title: "T", Body: "B",
		Repos: []DeliverRepo{{RepoID: "repo-a", Branch: "marag/run-2"}, {RepoID: "repo-b", Branch: ""}},
	})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(opened) != 1 || opened[0].RepoID != "repo-a" {
		t.Fatalf("opened = %+v", opened)
	}
	env, body, _ := events.Unmarshal(fp.recs[len(fp.recs)-1].value)
	if env.Type != "run.pr.opened" || !strings.Contains(string(body), "repo-b") {
		t.Fatalf("opened event should report failed repo-b: %s", body)
	}
}

type fakeClaimer struct {
	claimed  map[string]bool
	released map[string]bool
	err      error
}

func (f *fakeClaimer) Claim(_ context.Context, key string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if f.claimed == nil {
		f.claimed = map[string]bool{}
	}
	if f.claimed[key] {
		return false, nil
	}
	f.claimed[key] = true
	return true, nil
}

func (f *fakeClaimer) Release(_ context.Context, key string) error {
	if f.released == nil {
		f.released = map[string]bool{}
	}
	f.released[key] = true
	delete(f.claimed, key)
	return nil
}

func TestDeliverSkipsAlreadyClaimed(t *testing.T) {
	prov := gitprovider.NewFake()
	claimer := &fakeClaimer{}
	pub := New(idRepos{}, func(string) (gitprovider.GitProvider, error) { return prov, nil },
		func(string) string { return "t" }, &fakeComp{}, &fakeProd{}).WithClaimer(claimer)
	evt := DeliverEvent{RunID: "run-1", Title: "T", Repos: []DeliverRepo{{RepoID: "repo-a", Branch: "b"}}}

	if _, err := pub.Deliver(context.Background(), evt); err != nil {
		t.Fatalf("first Deliver: %v", err)
	}
	if len(prov.Created) != 1 {
		t.Fatalf("first delivery should open 1 PR, got %d", len(prov.Created))
	}
	// Redelivery of the same run: skipped, no new PR.
	if _, err := pub.Deliver(context.Background(), evt); err != nil {
		t.Fatalf("second Deliver: %v", err)
	}
	if len(prov.Created) != 1 {
		t.Fatalf("redelivery opened a duplicate PR: %d total", len(prov.Created))
	}
}

func TestDeliverAllReposFailErrors(t *testing.T) {
	fp := &fakeProd{}
	fc := &fakeComp{}
	prov := gitprovider.NewFake()
	pub := New(idRepos{}, func(string) (gitprovider.GitProvider, error) { return prov, nil },
		func(string) string { return "tok" }, fc, fp)
	_, err := pub.Deliver(context.Background(), DeliverEvent{
		RunID: "run-3", Title: "T", Body: "B",
		Repos: []DeliverRepo{{RepoID: "repo-a", Branch: ""}, {RepoID: "repo-b", Branch: ""}},
	})
	if err == nil {
		t.Fatalf("expected error when all repos fail")
	}
}

// TestDeliverReleasesClaimOnTotalFailure (H1): a fully-failed delivery must
// release its claim so a replay can retry — otherwise the run strands forever.
func TestDeliverReleasesClaimOnTotalFailure(t *testing.T) {
	claimer := &fakeClaimer{}
	pub := New(idRepos{}, func(string) (gitprovider.GitProvider, error) { return gitprovider.NewFake(), nil },
		func(string) string { return "t" }, &fakeComp{}, &fakeProd{}).WithClaimer(claimer)
	evt := DeliverEvent{RunID: "run-x", Title: "T", Repos: []DeliverRepo{{RepoID: "repo-a", Branch: ""}}}

	if _, err := pub.Deliver(context.Background(), evt); err == nil {
		t.Fatal("expected error when all repos fail")
	}
	if !claimer.released["deliver:run-x"] {
		t.Fatal("claim was not released on total failure (run would strand)")
	}
	// A retry can now re-claim (not skipped).
	if _, err := pub.Deliver(context.Background(), evt); err == nil {
		t.Fatal("retry should attempt again, not be skipped as already-delivered")
	}
}
