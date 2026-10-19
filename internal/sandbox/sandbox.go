// Package sandbox provides a disposable workspace where agent stages write
// code and run commands. LocalSandbox is a temp git repo driven via os/exec;
// a Managed-Agents-hosted implementation can satisfy the same interface later.
package sandbox

import "context"

// Result is the outcome of running a command in the sandbox. A non-zero
// ExitCode is data, not an error — callers (QA) inspect failing builds.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Sandbox is a workspace for a single run's code changes.
type Sandbox interface {
	// Dir is the absolute workspace root.
	Dir() string
	// WriteFile writes content to a path relative to the root, creating parent
	// directories. Paths escaping the root are rejected.
	WriteFile(rel string, content []byte) error
	// ReadFile reads a path relative to the root. Paths escaping the root are rejected.
	ReadFile(rel string) ([]byte, error)
	// Run executes name+args with the workspace as the working directory.
	Run(ctx context.Context, name string, args ...string) (Result, error)
	// Diff returns the working-tree diff against HEAD (staged + unstaged).
	Diff(ctx context.Context) (string, error)
	// Commit stages all changes and commits them, returning the commit SHA.
	Commit(ctx context.Context, message string) (string, error)
	// Close removes the workspace.
	Close() error
}
