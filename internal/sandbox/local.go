package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// LocalSandbox is a temp git repo manipulated via os/exec.
type LocalSandbox struct {
	dir string
	// gitEnv carries git auth (GIT_ASKPASS + token) applied ONLY to the trusted
	// git clone/push exec — never to Run. Empty when no token was supplied.
	gitEnv []string
	// askpassDir holds the askpass helper, outside the workspace so untrusted
	// commands in the workspace can't read it; removed on Close.
	askpassDir string
	// execPrefix, when set, wraps every Run command in a jail launcher (e.g.
	// bwrap/firejail with --net none) so untrusted commands can't reach the
	// network or the host. Empty = direct exec (dev/test only).
	execPrefix []string
}

// SetExecPrefix installs the jail launcher used to wrap untrusted Run commands.
func (s *LocalSandbox) SetExecPrefix(prefix []string) {
	s.execPrefix = append([]string(nil), prefix...)
}

// untrustedEnvAllow is the deny-by-default allowlist of environment variable
// names passed through to untrusted Run commands. It carries what the Go
// toolchain needs to build/test (incl. module fetches via GOPROXY) and basic
// locale/tmp settings — and deliberately nothing that names or unlocks a
// secret. Note GO-prefixed matching is an EXACT list, not a prefix, so
// GOOGLE_APPLICATION_CREDENTIALS (a Vertex secret) never slips through.
var untrustedEnvAllow = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"TZ": true, "LANG": true, "PWD": true, "TMPDIR": true, "TEMP": true, "TMP": true,
	"HOSTNAME": true, "TERM": true,
	// Go toolchain (none of these are secrets):
	"GOPATH": true, "GOCACHE": true, "GOMODCACHE": true, "GOROOT": true,
	"GOPROXY": true, "GOFLAGS": true, "GOSUMDB": true, "GONOSUMCHECK": true,
	"GOPRIVATE": true, "GOTOOLCHAIN": true, "GO111MODULE": true, "GOOS": true, "GOARCH": true,
}

// untrustedEnv builds the scrubbed environment for untrusted Run commands:
// only the allowlisted vars from the worker's environment, plus LC_* locale
// vars. Everything else — CONSUL_*, ANTHROPIC_*, *_TOKEN/_KEY/_SECRET, GCP ADC,
// etc. — is dropped so untrusted code cannot read or locate platform secrets.
func untrustedEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if untrustedEnvAllow[k] || strings.HasPrefix(k, "LC_") {
			out = append(out, kv)
		}
	}
	return out
}

// GitUserURL injects the provider's fixed username (NOT the secret token) into
// an https URL so git prompts only for the password, which is supplied out of
// band via GIT_ASKPASS. An empty token or non-https URL is returned unchanged.
// The username (x-access-token / oauth2) is a public literal, safe to persist.
func GitUserURL(remoteURL, provider, token string) string {
	if token == "" {
		return remoteURL
	}
	const prefix = "https://"
	if !strings.HasPrefix(remoteURL, prefix) {
		return remoteURL
	}
	rest := strings.TrimPrefix(remoteURL, prefix)
	switch provider {
	case "github":
		return prefix + "x-access-token@" + rest
	case "gitlab":
		return prefix + "oauth2@" + rest
	default:
		return remoteURL
	}
}

var _ Sandbox = (*LocalSandbox)(nil)

// credentialURLRe matches the userinfo portion of an http(s) URL (e.g.
// https://x-access-token:TOKEN@host/...) so it can be redacted before the
// string is embedded in an error. Errors from this package can end up
// persisted to a Postgres checkpoint and a Kafka event, so raw source URLs
// and git command output must never carry a live credential.
var credentialURLRe = regexp.MustCompile(`(?i)(https?://)[^/@\s]*@`)

// redactCredentials replaces embedded URL userinfo (e.g. the auth token in a
// clone URL) with a redaction marker so it is safe to include in errors.
func redactCredentials(s string) string {
	return credentialURLRe.ReplaceAllString(s, "${1}***@")
}

// NewLocal creates a temp dir, initializes a git repo with a local identity,
// and makes an initial empty commit so Diff/Commit have a base.
func NewLocal(ctx context.Context) (*LocalSandbox, error) {
	dir, err := os.MkdirTemp("", "marag-sandbox-*")
	if err != nil {
		return nil, fmt.Errorf("sandbox mkdir: %w", err)
	}
	sb := &LocalSandbox{dir: dir}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "agent@multi-agent-rag.local"},
		{"config", "user.name", "multi-agent-rag"},
		{"commit", "--allow-empty", "-q", "-m", "init"},
	} {
		if _, err := sb.git(ctx, args...); err != nil {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("sandbox git %v: %w", args, err)
		}
	}
	return sb, nil
}

// NewLocalFromRepo clones source into a fresh temp workspace. source is any
// git-clonable location (a local path for tests, or a username-only HTTPS URL
// in production — e.g. https://x-access-token@host/...). When token != "" it is
// supplied to git via GIT_ASKPASS (never embedded in the URL or persisted in
// the workspace), so the origin remote and .git/config carry no secret.
func NewLocalFromRepo(ctx context.Context, source, token string) (*LocalSandbox, error) {
	dir, err := os.MkdirTemp("", "marag-sandbox-*")
	if err != nil {
		return nil, fmt.Errorf("sandbox mkdir: %w", err)
	}
	sb := &LocalSandbox{dir: dir}
	if token != "" {
		if err := sb.setupAskpass(token); err != nil {
			_ = os.RemoveAll(dir)
			return nil, err
		}
	}
	// Clone into dir (dir already exists and is empty — git clone allows that).
	cmd := exec.CommandContext(ctx, "git", "clone", "--quiet", source, dir)
	cmd.Env = append(os.Environ(), sb.gitEnv...)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir)
		_ = os.RemoveAll(sb.askpassDir)
		return nil, fmt.Errorf("sandbox clone %q: %w: %s", redactCredentials(source), err, redactCredentials(string(out)))
	}
	// Ensure a commit identity for any commits made in this workspace.
	for _, args := range [][]string{
		{"config", "user.email", "agent@multi-agent-rag.local"},
		{"config", "user.name", "multi-agent-rag"},
	} {
		if _, err := sb.git(ctx, args...); err != nil {
			_ = os.RemoveAll(dir)
			return nil, fmt.Errorf("sandbox git %v: %w", args, err)
		}
	}
	return sb, nil
}

// CreateBranch creates and checks out a new branch.
func (s *LocalSandbox) CreateBranch(ctx context.Context, name string) error {
	if _, err := s.git(ctx, "checkout", "-b", name); err != nil {
		return fmt.Errorf("sandbox create branch %q: %w", name, err)
	}
	return nil
}

// Push pushes branch to remote. force re-pushes (used on developer retries).
func (s *LocalSandbox) Push(ctx context.Context, remote, branch string, force bool) error {
	args := []string{"push", "--quiet"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, remote, branch)
	if _, err := s.git(ctx, args...); err != nil {
		return fmt.Errorf("sandbox push %s/%s: %w", remote, branch, err)
	}
	return nil
}

func (s *LocalSandbox) Dir() string { return s.dir }

// setupAskpass writes an askpass helper outside the workspace that echoes the
// token from an env var, and records the git-only auth env. The token lives
// only in that env var on the trusted git process — never in the workspace.
func (s *LocalSandbox) setupAskpass(token string) error {
	dir, err := os.MkdirTemp("", "marag-askpass-*")
	if err != nil {
		return fmt.Errorf("sandbox askpass mkdir: %w", err)
	}
	script := filepath.Join(dir, "askpass.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$MARAG_GIT_TOKEN\"\n"), 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("sandbox askpass write: %w", err)
	}
	s.askpassDir = dir
	s.gitEnv = []string{
		"GIT_ASKPASS=" + script,
		"MARAG_GIT_TOKEN=" + token,
		"GIT_TERMINAL_PROMPT=0",
	}
	return nil
}

func (s *LocalSandbox) Close() error {
	if s.askpassDir != "" {
		_ = os.RemoveAll(s.askpassDir)
	}
	if err := os.RemoveAll(s.dir); err != nil {
		return fmt.Errorf("sandbox close: %w", err)
	}
	return nil
}

// resolve confines rel to the workspace root, rejecting escapes.
func (s *LocalSandbox) resolve(rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("sandbox: absolute path not allowed: %q", rel)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("sandbox: path escapes workspace: %q", rel)
	}
	abs := filepath.Join(s.dir, clean)
	// Defense in depth: the resolved path must remain under the root.
	if abs != s.dir && !strings.HasPrefix(abs, s.dir+string(filepath.Separator)) {
		return "", fmt.Errorf("sandbox: path escapes workspace: %q", rel)
	}

	// Symlink-aware confinement: a command run in the sandbox could plant a
	// symlink; ensure the real path (nearest existing ancestor resolved) stays
	// within the resolved workspace root. This closes the lexical-only gap
	// above (e.g. `ln -s /outside evil` then WriteFile("evil/x", ...)).
	// Note: there is a residual TOCTOU window between this check and the
	// actual os.WriteFile/os.ReadFile call (a symlink could be swapped in
	// between); acceptable for this sandbox's threat model.
	root, err := filepath.EvalSymlinks(s.dir)
	if err != nil {
		return "", fmt.Errorf("sandbox resolve root: %w", err)
	}
	real := abs
	for {
		resolved, rerr := filepath.EvalSymlinks(real)
		if rerr == nil {
			real = resolved
			break
		}
		if !os.IsNotExist(rerr) {
			return "", fmt.Errorf("sandbox resolve %q: %w", rel, rerr)
		}
		parent := filepath.Dir(real)
		if parent == real {
			break
		}
		real = parent
	}
	if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return "", fmt.Errorf("sandbox: path escapes workspace via symlink: %q", rel)
	}
	return abs, nil
}

func (s *LocalSandbox) WriteFile(rel string, content []byte) error {
	abs, err := s.resolve(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("sandbox mkdir: %w", err)
	}
	if err := os.WriteFile(abs, content, 0o644); err != nil {
		return fmt.Errorf("sandbox write %q: %w", rel, err)
	}
	return nil
}

func (s *LocalSandbox) ReadFile(rel string) ([]byte, error) {
	abs, err := s.resolve(rel)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("sandbox read %q: %w", rel, err)
	}
	return b, nil
}

// Run executes name+args in the workspace, wrapped in the jail launcher
// (execPrefix) when configured — this is the UNTRUSTED path (agent run_command
// + repo `go test`). It never receives the git auth env. A non-zero exit is
// returned in Result.ExitCode with a nil error; only a start failure or ctx
// cancellation yields a non-nil error.
func (s *LocalSandbox) Run(ctx context.Context, name string, args ...string) (Result, error) {
	execName, execArgs := name, args
	if len(s.execPrefix) > 0 {
		execName = s.execPrefix[0]
		execArgs = append(append([]string{}, s.execPrefix[1:]...), append([]string{name}, args...)...)
	}
	cmd := exec.CommandContext(ctx, execName, execArgs...)
	cmd.Dir = s.dir
	// SECURITY: untrusted commands run with a scrubbed, deny-by-default env — not
	// the worker's. This keeps platform credentials out of reach: a team member's
	// crafted request cannot read CONSUL_URL to curl the secret config blob, nor
	// inherit GOOGLE_APPLICATION_CREDENTIALS / *_TOKEN / *_KEY / *_SECRET. Only the
	// vars the Go toolchain needs pass through. The git auth env is likewise never
	// applied to Run (it is scoped to the trusted git clone/push only).
	cmd.Env = untrustedEnv()
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		if ctx.Err() != nil {
			return res, fmt.Errorf("sandbox run %q: %w", name, ctx.Err())
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, nil
		}
		return res, fmt.Errorf("sandbox run %q: %w", name, err)
	}
	return res, nil
}

// Diff stages all changes and returns the diff against HEAD.
func (s *LocalSandbox) Diff(ctx context.Context) (string, error) {
	if _, err := s.git(ctx, "add", "-A"); err != nil {
		return "", err
	}
	out, err := s.git(ctx, "diff", "--cached")
	if err != nil {
		return "", err
	}
	return out, nil
}

// Commit stages all changes, commits, and returns the new HEAD SHA.
func (s *LocalSandbox) Commit(ctx context.Context, message string) (string, error) {
	if _, err := s.git(ctx, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := s.git(ctx, "commit", "-q", "-m", message); err != nil {
		return "", err
	}
	out, err := s.git(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// git runs a trusted git subcommand in the workspace and returns combined
// stdout. It carries the git auth env (GIT_ASKPASS + token) so clone/push/fetch
// authenticate; this is NOT the jailed path (git ops need the network + token).
func (s *LocalSandbox) git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = s.dir
	cmd.Env = append(os.Environ(), s.gitEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %v: %w: %s", args, err, string(out))
	}
	return string(out), nil
}
