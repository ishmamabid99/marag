// Package qa runs deterministic build/vet/test checks in a sandbox — the free,
// fast gate that runs before any semantic (LLM) review.
package qa

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/fin/multi-agent-rag/internal/sandbox"
)

// StepResult is the outcome of one deterministic check.
type StepResult struct {
	Passed bool
	Output string
}

// Result aggregates the deterministic QA checks.
type Result struct {
	AllPassed bool
	Build     StepResult
	Vet       StepResult
	Test      StepResult
	// FailingTests is best-effort: names parsed from `--- FAIL:` lines. It can
	// be empty even when Test.Passed is false — e.g. when a _test.go file fails
	// to compile, `go test` prints `FAIL <pkg> [build failed]` with no per-test
	// lines. Callers must treat Test.Passed (not len(FailingTests)) as the
	// authoritative pass/fail signal and fall back to Test.Output for detail.
	FailingTests []string
	// EnvUnavailable is set when the code compiles and vets cleanly but the
	// tests fail with a connectivity error (e.g. "connection refused" dialing a
	// database) — i.e. the tests need an external service the sandbox doesn't
	// provide. This is an ENVIRONMENT gap, not a code defect: retrying the agent
	// cannot fix it, so callers should stop the dev loop and report it distinctly
	// rather than blaming the change.
	EnvUnavailable bool
}

var failLineRE = regexp.MustCompile(`(?m)^\s*--- FAIL: (\S+)`)

// envFailureRE matches test output indicating an unreachable external service
// (a network dependency the tests try to contact) rather than a code defect.
var envFailureRE = regexp.MustCompile(`(?i)(connection refused|dial tcp|no such host|network is unreachable|i/o timeout|connection reset by peer|context deadline exceeded)`)

// looksLikeEnvFailure reports whether test output indicates the tests could not
// run because an external service was unavailable (vs. a genuine test failure).
func looksLikeEnvFailure(output string) bool {
	return envFailureRE.MatchString(output)
}

// RunChecks runs go build, then (if it compiles) go vet and go test, in the
// sandbox. A failing check is reflected in the Result, not returned as an
// error; an error is returned only if a command cannot be executed.
func RunChecks(ctx context.Context, sb sandbox.Sandbox) (Result, error) {
	var r Result

	build, err := sb.Run(ctx, "go", "build", "./...")
	if err != nil {
		return r, fmt.Errorf("qa build: %w", err)
	}
	r.Build = StepResult{Passed: build.ExitCode == 0, Output: combine(build)}
	if !r.Build.Passed {
		// Code does not compile — vet and test cannot run meaningfully.
		return r, nil
	}

	vet, err := sb.Run(ctx, "go", "vet", "./...")
	if err != nil {
		return r, fmt.Errorf("qa vet: %w", err)
	}
	r.Vet = StepResult{Passed: vet.ExitCode == 0, Output: combine(vet)}

	test, err := sb.Run(ctx, "go", "test", "./...")
	if err != nil {
		return r, fmt.Errorf("qa test: %w", err)
	}
	r.Test = StepResult{Passed: test.ExitCode == 0, Output: combine(test)}
	r.FailingTests = parseFailingTests(r.Test.Output)

	r.AllPassed = r.Build.Passed && r.Vet.Passed && r.Test.Passed

	// Smarter QA: build + vet passed but the tests failed with a connectivity
	// error → the tests need an external service the sandbox doesn't provide.
	// Flag it as an environment gap so the caller stops retrying (the agent
	// can't conjure a database) and reports it distinctly from a code failure.
	if !r.Test.Passed && r.Build.Passed && r.Vet.Passed && looksLikeEnvFailure(r.Test.Output) {
		r.EnvUnavailable = true
	}
	return r, nil
}

func combine(res sandbox.Result) string {
	return strings.TrimSpace(res.Stdout + "\n" + res.Stderr)
}

// parseFailingTests extracts test names from `--- FAIL: <name>` lines.
func parseFailingTests(output string) []string {
	matches := failLineRE.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(matches))
	var names []string
	for _, m := range matches {
		name := m[1]
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}
