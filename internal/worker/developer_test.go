package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fin/multi-agent-rag/internal/codeagent"
	"github.com/fin/multi-agent-rag/internal/sandbox"
)

const wGoMod = "module devtest\n\ngo 1.21\n"
const wPassTest = "package devtest\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatalf(\"got %d\", Add(2, 3))\n\t}\n}\n"

func wgit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// seedBareRepo returns a bare repo with one commit on main.
func seedBareRepo(t *testing.T) string {
	t.Helper()
	bare := t.TempDir()
	wgit(t, "", "init", "--bare", "-b", "main", bare)
	work := t.TempDir()
	wgit(t, "", "clone", bare, work)
	wgit(t, work, "config", "user.email", "s@t.local")
	wgit(t, work, "config", "user.name", "s")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wgit(t, work, "add", "-A")
	wgit(t, work, "commit", "-q", "-m", "seed")
	wgit(t, work, "push", "-q", "origin", "main")
	return bare
}

func devWithResolver(agent codeagent.CodeAgent, bare string, maxIter int) *Developer {
	d := NewDeveloper(agent, &FakeResolver{Checkouts: map[string]RepoCheckout{"repo-a": {CloneURL: bare, BaseBranch: "main"}}})
	d.maxIter = maxIter
	return d
}

func writeModule(sb sandbox.Sandbox, impl string) error {
	if err := sb.WriteFile("go.mod", []byte(wGoMod)); err != nil {
		return err
	}
	if err := sb.WriteFile("calc.go", []byte(impl)); err != nil {
		return err
	}
	return sb.WriteFile("calc_test.go", []byte(wPassTest))
}

func TestDeveloperClonesImplementsPushes(t *testing.T) {
	bare := seedBareRepo(t)
	agent := codeagent.NewFake(func(_ context.Context, task codeagent.Task, sb sandbox.Sandbox) (codeagent.Outcome, error) {
		if err := writeModule(sb, "package devtest\n\nfunc Add(a, b int) int { return a + b }\n"); err != nil {
			return codeagent.Outcome{}, err
		}
		return codeagent.Outcome{Summary: "done"}, nil
	})
	res, err := devWithResolver(agent, bare, 3).Handle(context.Background(),
		StageInput{RunID: "run-1", Stage: "developing", Spec: "add Add", RepoIDs: []string{"repo-a"}})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var art DeveloperArtifact
	if err := json.Unmarshal(res.ArtifactJSON, &art); err != nil {
		t.Fatalf("artifact: %v", err)
	}
	if !art.TestsPassed || art.BranchName != "marag/run-1" || len(art.HeadCommit) < 7 {
		t.Fatalf("artifact = %+v", art)
	}
	// The branch was pushed to the bare repo with the implementation.
	verify := t.TempDir()
	wgit(t, "", "clone", "--quiet", "-b", "marag/run-1", bare, verify)
	if _, err := os.Stat(filepath.Join(verify, "calc.go")); err != nil {
		t.Fatalf("pushed branch missing calc.go: %v", err)
	}
}

func TestDeveloperExhaustsAndFails(t *testing.T) {
	bare := seedBareRepo(t)
	agent := codeagent.NewFake(func(_ context.Context, _ codeagent.Task, sb sandbox.Sandbox) (codeagent.Outcome, error) {
		if err := writeModule(sb, "package devtest\n\nfunc Add(a, b int) int { return a - b }\n"); err != nil {
			return codeagent.Outcome{}, err
		}
		return codeagent.Outcome{Summary: "wrong"}, nil
	})
	_, err := devWithResolver(agent, bare, 2).Handle(context.Background(),
		StageInput{RunID: "run-2", Stage: "developing", Spec: "x", RepoIDs: []string{"repo-a"}})
	if !errors.Is(err, ErrDevExhausted) {
		t.Fatalf("err = %v, want ErrDevExhausted", err)
	}
	// Exhaustion must not push: the branch should not exist on the remote.
	if out := lsRemoteRef(t, bare, "marag/run-2"); out != "" {
		t.Fatalf("expected no marag/run-2 ref pushed, got: %q", out)
	}
}

// lsRemoteRef returns the trimmed `git ls-remote <repo> <ref>` output (empty
// if the ref doesn't exist).
func lsRemoteRef(t *testing.T, repo, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "ls-remote", repo, ref)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git ls-remote %s %s: %v\n%s", repo, ref, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestDeveloperLoopsUntilTestsPass(t *testing.T) {
	bare := seedBareRepo(t)
	agent := codeagent.NewFake(func(_ context.Context, task codeagent.Task, sb sandbox.Sandbox) (codeagent.Outcome, error) {
		if task.Attempt == 1 {
			if err := writeModule(sb, "package devtest\n\nfunc Add(a, b int) int { return a - b }\n"); err != nil {
				return codeagent.Outcome{}, err
			}
			return codeagent.Outcome{Summary: "wrong"}, nil
		}
		if task.Feedback == "" {
			t.Fatalf("attempt %d: expected feedback from failed QA, got empty", task.Attempt)
		}
		if err := writeModule(sb, "package devtest\n\nfunc Add(a, b int) int { return a + b }\n"); err != nil {
			return codeagent.Outcome{}, err
		}
		return codeagent.Outcome{Summary: "fixed"}, nil
	})
	res, err := devWithResolver(agent, bare, 3).Handle(context.Background(),
		StageInput{RunID: "run-loop", Stage: "developing", Spec: "add Add", RepoIDs: []string{"repo-a"}})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var art DeveloperArtifact
	if err := json.Unmarshal(res.ArtifactJSON, &art); err != nil {
		t.Fatalf("artifact: %v", err)
	}
	if art.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", art.Iterations)
	}
	if !art.TestsPassed {
		t.Fatalf("TestsPassed = false, want true")
	}
	if art.BranchName != "marag/run-loop" {
		t.Fatalf("BranchName = %q, want marag/run-loop", art.BranchName)
	}
	verify := t.TempDir()
	wgit(t, "", "clone", "--quiet", "-b", "marag/run-loop", bare, verify)
	got, err := os.ReadFile(filepath.Join(verify, "calc.go"))
	if err != nil {
		t.Fatalf("read pushed calc.go: %v", err)
	}
	if !strings.Contains(string(got), "a + b") {
		t.Fatalf("pushed calc.go does not contain corrected impl: %s", got)
	}
}

// devMulti builds a Developer whose resolver maps each id→its bare repo.
func devMulti(agent codeagent.CodeAgent, repos map[string]string, maxIter int) *Developer {
	checkouts := map[string]RepoCheckout{}
	for id, bare := range repos {
		checkouts[id] = RepoCheckout{CloneURL: bare, BaseBranch: "main"}
	}
	d := NewDeveloper(agent, &FakeResolver{Checkouts: checkouts})
	d.maxIter = maxIter
	return d
}

func TestDeveloperMultiRepoAllSucceed(t *testing.T) {
	bareA, bareB := seedBareRepo(t), seedBareRepo(t)
	agent := codeagent.NewFake(func(_ context.Context, task codeagent.Task, sb sandbox.Sandbox) (codeagent.Outcome, error) {
		if err := writeModule(sb, "package devtest\n\nfunc Add(a, b int) int { return a + b }\n"); err != nil {
			return codeagent.Outcome{}, err
		}
		return codeagent.Outcome{Summary: "impl " + task.TargetRepoID}, nil
	})
	res, err := devMulti(agent, map[string]string{"repo-a": bareA, "repo-b": bareB}, 2).Handle(context.Background(),
		StageInput{RunID: "run-1", Stage: "developing", Spec: "feature", RepoIDs: []string{"repo-a", "repo-b"}})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	var art DeveloperArtifact
	if err := json.Unmarshal(res.ArtifactJSON, &art); err != nil {
		t.Fatalf("artifact: %v", err)
	}
	if len(art.Repos) != 2 || !art.Repos[0].TestsPassed || !art.Repos[1].TestsPassed {
		t.Fatalf("repos = %+v", art.Repos)
	}
	// Primary fields mirror the first successful repo (single-repo publisher compat).
	if art.BranchName != "marag/run-1" || !art.TestsPassed {
		t.Fatalf("primary = %+v", art)
	}
	// Both branches pushed.
	for _, bare := range []string{bareA, bareB} {
		v := t.TempDir()
		wgit(t, "", "clone", "--quiet", "-b", "marag/run-1", bare, v)
	}
	// The 2nd repo's agent call saw the 1st repo's work.
	sawPrior := false
	for _, c := range agent.Calls() {
		if strings.Contains(c.PriorRepoWork, "repo-a") {
			sawPrior = true
		}
	}
	if !sawPrior {
		t.Fatalf("second repo did not receive prior-repo work: %+v", agent.Calls())
	}
}

func TestDeveloperMultiRepoBestEffortPartialFailure(t *testing.T) {
	bareA, bareB := seedBareRepo(t), seedBareRepo(t)
	agent := codeagent.NewFake(func(_ context.Context, task codeagent.Task, sb sandbox.Sandbox) (codeagent.Outcome, error) {
		impl := "func Add(a, b int) int { return a + b }" // repo-a: correct
		if task.TargetRepoID == "repo-b" {
			impl = "func Add(a, b int) int { return a - b }" // repo-b: wrong → QA fails
		}
		if err := writeModule(sb, "package devtest\n\n"+impl+"\n"); err != nil {
			return codeagent.Outcome{}, err
		}
		return codeagent.Outcome{Summary: "impl " + task.TargetRepoID}, nil
	})
	res, err := devMulti(agent, map[string]string{"repo-a": bareA, "repo-b": bareB}, 2).Handle(context.Background(),
		StageInput{RunID: "run-2", Stage: "developing", Spec: "feature", RepoIDs: []string{"repo-a", "repo-b"}})
	if err != nil {
		t.Fatalf("Handle (should succeed best-effort): %v", err)
	}
	var art DeveloperArtifact
	_ = json.Unmarshal(res.ArtifactJSON, &art)
	if len(art.Repos) != 2 {
		t.Fatalf("repos = %+v", art.Repos)
	}
	var a, b RepoResult
	for _, r := range art.Repos {
		if r.RepoID == "repo-a" {
			a = r
		} else {
			b = r
		}
	}
	if !a.TestsPassed || b.TestsPassed || b.Failure == "" {
		t.Fatalf("expected repo-a pass, repo-b fail-with-reason: a=%+v b=%+v", a, b)
	}
	if art.BranchName != "marag/run-2" { // primary = first success (repo-a)
		t.Fatalf("primary branch = %q", art.BranchName)
	}
	if art.RepoID != "repo-a" {
		t.Fatalf("RepoID = %q, want repo-a (the succeeding repo)", art.RepoID)
	}
}

func TestDeveloperMultiRepoAllFail(t *testing.T) {
	bareA := seedBareRepo(t)
	agent := codeagent.NewFake(func(_ context.Context, _ codeagent.Task, sb sandbox.Sandbox) (codeagent.Outcome, error) {
		_ = writeModule(sb, "package devtest\n\nfunc Add(a, b int) int { return a - b }\n") // wrong
		return codeagent.Outcome{Summary: "wrong"}, nil
	})
	_, err := devMulti(agent, map[string]string{"repo-a": bareA}, 2).Handle(context.Background(),
		StageInput{RunID: "run-3", Stage: "developing", Spec: "x", RepoIDs: []string{"repo-a"}})
	if !errors.Is(err, ErrDevExhausted) {
		t.Fatalf("err = %v, want ErrDevExhausted (all repos failed)", err)
	}
}

func TestDeveloperNoRepoErrors(t *testing.T) {
	agent := codeagent.NewFake(func(_ context.Context, _ codeagent.Task, _ sandbox.Sandbox) (codeagent.Outcome, error) {
		return codeagent.Outcome{}, nil
	})
	_, err := NewDeveloper(agent, &FakeResolver{}).Handle(context.Background(),
		StageInput{RunID: "run-3", Stage: "developing", Spec: "x", RepoIDs: nil})
	if err == nil {
		t.Fatalf("expected error when no repo id")
	}
}

func TestDeveloperRole(t *testing.T) {
	if NewDeveloper(codeagent.NewFake(nil), &FakeResolver{}).Role() != "developer" {
		t.Fatalf("role mismatch")
	}
}

func TestDeveloperEscalatesToOpusOnDraftFailure(t *testing.T) {
	bare := seedBareRepo(t)
	draft := codeagent.NewFake(func(_ context.Context, _ codeagent.Task, sb sandbox.Sandbox) (codeagent.Outcome, error) {
		// wrong impl: Add returns a-b, so the pass-test fails every attempt.
		return codeagent.Outcome{Summary: "draft"}, writeModule(sb, "package devtest\n\nfunc Add(a, b int) int { return a - b }\n")
	})
	var escalated bool
	esc := codeagent.NewFake(func(_ context.Context, _ codeagent.Task, sb sandbox.Sandbox) (codeagent.Outcome, error) {
		escalated = true
		return codeagent.Outcome{Summary: "escalated"}, writeModule(sb, "package devtest\n\nfunc Add(a, b int) int { return a + b }\n")
	})
	d := devWithResolver(draft, bare, 2).WithEscalation(esc)
	res, err := d.Handle(context.Background(),
		StageInput{RunID: "run-esc", Stage: "developing", Spec: "add Add", RepoIDs: []string{"repo-a"}})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if !escalated {
		t.Fatal("escalation agent was not invoked")
	}
	var art DeveloperArtifact
	if err := json.Unmarshal(res.ArtifactJSON, &art); err != nil {
		t.Fatalf("artifact: %v", err)
	}
	if !art.TestsPassed || art.Summary != "escalated" {
		t.Fatalf("artifact = %+v", art)
	}
}

func TestDeveloperLeadsWithEscalationOnSemanticRetry(t *testing.T) {
	bare := seedBareRepo(t)
	var draftCalled bool
	draft := codeagent.NewFake(func(_ context.Context, _ codeagent.Task, sb sandbox.Sandbox) (codeagent.Outcome, error) {
		draftCalled = true
		return codeagent.Outcome{Summary: "draft"}, writeModule(sb, "package devtest\n\nfunc Add(a, b int) int { return a + b }\n")
	})
	var escFeedback string
	esc := codeagent.NewFake(func(_ context.Context, task codeagent.Task, sb sandbox.Sandbox) (codeagent.Outcome, error) {
		escFeedback = task.Feedback
		return codeagent.Outcome{Summary: "escalated"}, writeModule(sb, "package devtest\n\nfunc Add(a, b int) int { return a + b }\n")
	})
	d := devWithResolver(draft, bare, 2).WithEscalation(esc)
	qaJSON := []byte(`{"qa_passed":false,"issues":["missing overflow handling"],"rationale":"incomplete"}`)
	res, err := d.Handle(context.Background(), StageInput{
		RunID: "run-sem", Stage: "developing", Spec: "add Add", RepoIDs: []string{"repo-a"},
		Attempt: 2, PriorArtifacts: map[string][]byte{"qa": qaJSON},
	})
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if draftCalled {
		t.Fatal("on a semantic retry the draft agent must NOT lead")
	}
	if !strings.Contains(escFeedback, "missing overflow handling") {
		t.Fatalf("escalation agent should receive the QA issues; got %q", escFeedback)
	}
	var art DeveloperArtifact
	_ = json.Unmarshal(res.ArtifactJSON, &art)
	if !art.TestsPassed || art.Summary != "escalated" {
		t.Fatalf("artifact = %+v", art)
	}
}

func TestDeveloperEnvUnavailableProceeds(t *testing.T) {
	bare := seedBareRepo(t)
	agent := codeagent.NewFake(func(_ context.Context, _ codeagent.Task, sb sandbox.Sandbox) (codeagent.Outcome, error) {
		if err := sb.WriteFile("go.mod", []byte(wGoMod)); err != nil {
			return codeagent.Outcome{}, err
		}
		if err := sb.WriteFile("calc.go", []byte("package devtest\n\nfunc Add(a, b int) int { return a + b }\n")); err != nil {
			return codeagent.Outcome{}, err
		}
		// Compiles + vets cleanly, but the test needs a service that isn't there.
		envTest := "package devtest\n\nimport (\n\t\"net\"\n\t\"testing\"\n)\n\nfunc TestNeedsService(t *testing.T) {\n\tc, err := net.Dial(\"tcp\", \"127.0.0.1:1\")\n\tif err != nil {\n\t\tt.Fatalf(\"%v\", err)\n\t}\n\t_ = c.Close()\n}\n"
		if err := sb.WriteFile("calc_test.go", []byte(envTest)); err != nil {
			return codeagent.Outcome{}, err
		}
		return codeagent.Outcome{Summary: "added Add"}, nil
	})
	res, err := devWithResolver(agent, bare, 3).Handle(context.Background(),
		StageInput{RunID: "run-env", Stage: "developing", Spec: "add Add", RepoIDs: []string{"repo-a"}})
	if err != nil {
		t.Fatalf("env gap should proceed to a PR, not fail: %v", err)
	}
	var art DeveloperArtifact
	if err := json.Unmarshal(res.ArtifactJSON, &art); err != nil {
		t.Fatalf("artifact: %v", err)
	}
	if art.TestsPassed {
		t.Fatal("tests did not pass (env gap) — TestsPassed must be false")
	}
	if !strings.Contains(art.QANote, "external service") {
		t.Fatalf("expected an env caveat note, got %q", art.QANote)
	}
	if len(art.Repos) != 1 || !art.Repos[0].TestsUnverified {
		t.Fatalf("expected repo TestsUnverified=true, got %+v", art.Repos)
	}
	// Shipped despite unverified tests: the branch must be pushed.
	if lsRemoteRef(t, bare, "marag/run-env") == "" {
		t.Fatal("env-unverified change should still be pushed")
	}
}

func TestRoutedTier(t *testing.T) {
	cases := []struct{ json, want string }{
		{"", "trivial"}, // no design ⟺ trivial run
		{`{"file_changes":[{"complexity_hint":"trivial"}]}`, "trivial"},
		{`{"file_changes":[{"complexity_hint":"trivial"},{"complexity_hint":"standard"}]}`, "standard"},
		{`{"file_changes":[{"complexity_hint":"standard"},{"complexity_hint":"complex"}]}`, "complex"},
		{`{"file_changes":[]}`, "standard"}, // undecidable → safe middle
		{`not json`, "standard"},
	}
	for _, c := range cases {
		if got := routedTier([]byte(c.json)); got != c.want {
			t.Errorf("routedTier(%q)=%q want %q", c.json, got, c.want)
		}
	}
}

func TestSelectAgentsRouting(t *testing.T) {
	cheap := codeagent.NewFake(nil)
	draft := codeagent.NewFake(nil)
	esc := codeagent.NewFake(nil)
	d := NewDeveloper(draft, &FakeResolver{}).WithEscalation(esc).WithCheapAgent(cheap)

	// trivial (no design) → cheap leads, draft backs up.
	if lead, backup := d.selectAgents(nil); lead != cheap || backup != draft {
		t.Fatalf("trivial routing wrong")
	}
	// standard → draft leads, escalation backs up (default ladder).
	if lead, backup := d.selectAgents([]byte(`{"file_changes":[{"complexity_hint":"standard"}]}`)); lead != draft || backup != esc {
		t.Fatalf("standard routing wrong")
	}
	// complex → lead with escalation (Opus) directly, no backup.
	if lead, backup := d.selectAgents([]byte(`{"file_changes":[{"complexity_hint":"complex"}]}`)); lead != esc || backup != nil {
		t.Fatalf("complex routing wrong")
	}

	// Back-compat: no cheap agent wired → trivial falls back to draft lead.
	d2 := NewDeveloper(draft, &FakeResolver{}).WithEscalation(esc)
	if lead, _ := d2.selectAgents(nil); lead != draft {
		t.Fatalf("no cheap agent should fall back to draft lead")
	}
}
