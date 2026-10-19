package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newLocal(t *testing.T) *LocalSandbox {
	t.Helper()
	sb, err := NewLocal(context.Background())
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })
	return sb
}

func TestLocalWriteReadRoundTrip(t *testing.T) {
	sb := newLocal(t)
	if err := sb.WriteFile("pkg/foo/bar.go", []byte("package foo\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := sb.ReadFile("pkg/foo/bar.go")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "package foo\n" {
		t.Fatalf("content = %q", got)
	}
	// The file physically exists under the workspace root.
	if _, err := os.Stat(filepath.Join(sb.Dir(), "pkg/foo/bar.go")); err != nil {
		t.Fatalf("stat: %v", err)
	}
}

func TestLocalRejectsPathEscape(t *testing.T) {
	sb := newLocal(t)
	for _, bad := range []string{"../escape.go", "a/../../escape.go", "/etc/passwd"} {
		if err := sb.WriteFile(bad, []byte("x")); err == nil {
			t.Fatalf("WriteFile(%q) = nil, want error", bad)
		}
		if _, err := sb.ReadFile(bad); err == nil {
			t.Fatalf("ReadFile(%q) = nil, want error", bad)
		}
	}
	// The escape target was not created anywhere.
	if _, err := os.Stat(filepath.Join(filepath.Dir(sb.Dir()), "escape.go")); !os.IsNotExist(err) {
		t.Fatalf("escape file exists: %v", err)
	}
}

func TestLocalCloseRemovesDir(t *testing.T) {
	sb := newLocal(t)
	dir := sb.Dir()
	if err := sb.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("dir still exists after Close: %v", err)
	}
}

func TestLocalRunCapturesOutputAndExit(t *testing.T) {
	sb := newLocal(t)
	ctx := context.Background()

	res, err := sb.Run(ctx, "sh", "-c", "echo out; echo err 1>&2")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 || !contains(res.Stdout, "out") || !contains(res.Stderr, "err") {
		t.Fatalf("res = %+v", res)
	}

	// Non-zero exit is DATA, not a Go error.
	res, err = sb.Run(ctx, "sh", "-c", "exit 3")
	if err != nil {
		t.Fatalf("Run non-zero returned error: %v", err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want 3", res.ExitCode)
	}

	// A process that cannot start IS an error.
	if _, err := sb.Run(ctx, "definitely-not-a-real-binary-xyz"); err == nil {
		t.Fatalf("Run(missing binary) = nil, want error")
	}
}

// SECURITY: untrusted Run commands must NOT inherit platform secrets or the
// pointers to them (CONSUL_URL, GCP ADC). They must still get what the Go
// toolchain needs (PATH, GOPROXY, …). This is the "team can't reach the keys"
// guarantee at the sandbox boundary.
func TestLocalRunScrubsSecretEnv(t *testing.T) {
	// Secrets / pointers-to-secrets that must be dropped.
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-SHOULD-NOT-LEAK")
	t.Setenv("CONSUL_URL", "consul:8500")
	t.Setenv("GITHUB_TOKEN", "ghp_SHOULD_NOT_LEAK")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/run/secrets/gcp.json")
	// A benign toolchain var that must pass through.
	t.Setenv("GOPROXY", "https://proxy.example")

	sb := newLocal(t)
	res, err := sb.Run(context.Background(), "sh", "-c", "env")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := res.Stdout
	for _, leaked := range []string{"ANTHROPIC_API_KEY", "sk-ant-SHOULD-NOT-LEAK",
		"CONSUL_URL", "GITHUB_TOKEN", "ghp_SHOULD_NOT_LEAK", "GOOGLE_APPLICATION_CREDENTIALS"} {
		if strings.Contains(out, leaked) {
			t.Errorf("untrusted Run env leaked %q; env output:\n%s", leaked, out)
		}
	}
	if !strings.Contains(out, "GOPROXY=https://proxy.example") {
		t.Errorf("toolchain var GOPROXY should pass through; env:\n%s", out)
	}
	if !strings.Contains(out, "PATH=") {
		t.Errorf("PATH must pass through (else the toolchain can't run); env:\n%s", out)
	}
}

func TestLocalCommitAndDiff(t *testing.T) {
	sb := newLocal(t)
	ctx := context.Background()

	if err := sb.WriteFile("a.txt", []byte("one\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	sha, err := sb.Commit(ctx, "add a")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(sha) < 7 {
		t.Fatalf("sha = %q", sha)
	}

	// A new uncommitted change shows up in Diff.
	if err := sb.WriteFile("a.txt", []byte("one\ntwo\n")); err != nil {
		t.Fatalf("WriteFile 2: %v", err)
	}
	diff, err := sb.Diff(ctx)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !contains(diff, "two") || !contains(diff, "a.txt") {
		t.Fatalf("diff missing change:\n%s", diff)
	}
}

func TestLocalRejectsSymlinkEscape(t *testing.T) {
	sb := newLocal(t)
	ctx := context.Background()

	victimDir := t.TempDir()

	if _, err := sb.Run(ctx, "ln", "-s", victimDir, "evil"); err != nil {
		t.Fatalf("Run(ln -s): %v", err)
	}

	if err := sb.WriteFile("evil/pwned.txt", []byte("owned")); err == nil {
		t.Fatalf("WriteFile(evil/pwned.txt) = nil, want error")
	}

	if _, err := os.Stat(filepath.Join(victimDir, "pwned.txt")); !os.IsNotExist(err) {
		t.Fatalf("victim file was created: %v", err)
	}
}

func TestLocalRunContextCancel(t *testing.T) {
	sb := newLocal(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := sb.Run(ctx, "sh", "-c", "sleep 5")
	if err == nil {
		t.Fatalf("Run under cancelled ctx = nil, want error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want wrapping context.DeadlineExceeded", err)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
