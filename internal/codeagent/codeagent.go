// Package codeagent defines the engine that turns a design into code changes
// inside a sandbox. LocalToolLoopAgent (Anthropic tool-use) is the default
// backend; a Managed-Agents backend can satisfy the same interface later.
package codeagent

import (
	"context"

	"github.com/fin/multi-agent-rag/internal/sandbox"
)

// Task is the developer's input for one implementation attempt.
type Task struct {
	Spec     string
	RepoIDs  []string
	Design   []byte // serialized DesignArtifact from the designer stage (may be empty)
	Feedback string // deterministic-QA failures from the previous attempt (empty on first)
	Attempt  int    // 1-based attempt number

	TargetRepoID  string // the repo currently being edited (multi-repo fan-out)
	PriorRepoWork string // summary of what earlier repos in this run already changed
}

// Outcome summarizes what an implementation attempt did.
type Outcome struct {
	Summary      string
	FilesChanged []string
}

// CodeAgent implements a change into the sandbox.
type CodeAgent interface {
	Implement(ctx context.Context, task Task, sb sandbox.Sandbox) (Outcome, error)
}
