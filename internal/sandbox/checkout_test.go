package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// seedBareRepo creates a bare repo with one commit on main and returns its path.
func seedBareRepo(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	bare := t.TempDir()
	mustGit(t, ctx, "", "init", "--bare", "-b", "main", bare)

	work := t.TempDir()
	mustGit(t, ctx, "", "clone", bare, work)
	mustGit(t, ctx, work, "config", "user.email", "seed@test.local")
	mustGit(t, ctx, work, "config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit(t, ctx, work, "add", "-A")
	mustGit(t, ctx, work, "commit", "-q", "-m", "seed")
	mustGit(t, ctx, work, "push", "-q", "origin", "main")
	return bare
}

func mustGit(t *testing.T, ctx context.Context, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestNewLocalFromRepoClones(t *testing.T) {
	bare := seedBareRepo(t)
	sb, err := NewLocalFromRepo(context.Background(), bare, "")
	if err != nil {
		t.Fatalf("NewLocalFromRepo: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })

	got, err := sb.ReadFile("README.md")
	if err != nil || string(got) != "hello\n" {
		t.Fatalf("README = %q err=%v", got, err)
	}
}

func TestNewLocalFromRepoBadSourceCleansUp(t *testing.T) {
	_, err := NewLocalFromRepo(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"), "")
	if err == nil {
		t.Fatalf("expected clone error for missing source")
	}
}

// TestNewLocalFromRepoRedactsToken guards against leaking an embedded clone
// credential (e.g. https://x-access-token:TOKEN@host/...) into the returned
// error, since these errors can be persisted to a Postgres checkpoint and a
// Kafka event.
func TestNewLocalFromRepoRedactsToken(t *testing.T) {
	_, err := NewLocalFromRepo(context.Background(), "https://x-access-token:SUPERSECRET@127.0.0.1:1/nope/nope.git", "")
	if err == nil {
		t.Fatalf("expected clone error for unreachable host")
	}
	if strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatalf("error leaks credential: %v", err)
	}
	if !strings.Contains(err.Error(), "***") {
		t.Fatalf("error does not contain redaction marker: %v", err)
	}
}

func TestCreateBranchAndPush(t *testing.T) {
	ctx := context.Background()
	bare := seedBareRepo(t)
	sb, err := NewLocalFromRepo(ctx, bare, "")
	if err != nil {
		t.Fatalf("NewLocalFromRepo: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })

	if err := sb.CreateBranch(ctx, "feat/add-x"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := sb.WriteFile("x.txt", []byte("x\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := sb.Commit(ctx, "add x"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := sb.Push(ctx, "origin", "feat/add-x", false); err != nil {
		t.Fatalf("Push: %v", err)
	}

	// Verify: a fresh clone of the bare repo has the pushed branch + file.
	verify := t.TempDir()
	mustGit(t, ctx, "", "clone", "--quiet", "-b", "feat/add-x", bare, verify)
	if _, err := os.Stat(filepath.Join(verify, "x.txt")); err != nil {
		t.Fatalf("pushed branch missing x.txt: %v", err)
	}
}

func TestGitUserURLOmitsToken(t *testing.T) {
	got := GitUserURL("https://github.com/o/r.git", "github", "SECRET")
	if strings.Contains(got, "SECRET") {
		t.Fatalf("token leaked into url: %s", got)
	}
	if got != "https://x-access-token@github.com/o/r.git" {
		t.Fatalf("github url = %s", got)
	}
	if got := GitUserURL("https://gitlab.com/o/r.git", "gitlab", "SECRET"); got != "https://oauth2@gitlab.com/o/r.git" {
		t.Fatalf("gitlab url = %s", got)
	}
	// empty token → unchanged; non-https → unchanged
	if GitUserURL("https://github.com/o/r.git", "github", "") != "https://github.com/o/r.git" {
		t.Fatal("empty token should not alter url")
	}
	if GitUserURL("/local/path", "github", "SECRET") != "/local/path" {
		t.Fatal("non-https should not be altered")
	}
}

func TestExecPrefixWraps(t *testing.T) {
	sb, err := NewLocal(context.Background())
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })

	sb.SetExecPrefix([]string{"/usr/bin/env"}) // env just execs its args
	res, err := sb.Run(context.Background(), "echo", "hi")
	if err != nil || strings.TrimSpace(res.Stdout) != "hi" {
		t.Fatalf("wrapped run: %q err %v", res.Stdout, err)
	}
	sb.SetExecPrefix([]string{"/nonexistent-jail-xyz"})
	if _, err := sb.Run(context.Background(), "echo", "hi"); err == nil {
		t.Fatal("bogus prefix must cause Run to fail (prefix not applied?)")
	}
}
