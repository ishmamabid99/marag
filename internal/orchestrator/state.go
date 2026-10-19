// Package orchestrator implements the run saga: a durable state machine that
// routes a feature request through complexity-appropriate agent stages.
package orchestrator

import (
	"fmt"
	"time"
)

// RunStatus is the coarse lifecycle state of a run.
type RunStatus string

const (
	StatusRequested  RunStatus = "requested"
	StatusRunning    RunStatus = "running"    // an agent stage is in flight
	StatusDelivering RunStatus = "delivering" // deliver.pr emitted, awaiting PR/MR
	StatusCompleted  RunStatus = "completed"
	StatusFailed     RunStatus = "failed"

	// StatusGatedWaitingHuman is a parked run awaiting a human approve/reject.
	// Not terminal (resumable via Engine.ResumeGate), not running.
	StatusGatedWaitingHuman RunStatus = "gated_waiting_human"
)

// Stage is an agent pipeline stage.
type Stage string

const (
	StageAnalyzing    Stage = "analyzing"
	StageViability    Stage = "viability"
	StageRisk         Stage = "risk"
	StageArchitecting Stage = "architecting"
	StageDesigning    Stage = "designing"
	StageDeveloping   Stage = "developing"
	StageQA           Stage = "qa"
)

// Complexity selects which stages run.
type Complexity string

const (
	ComplexityTrivial  Complexity = "trivial"
	ComplexityStandard Complexity = "standard"
	ComplexityComplex  Complexity = "complex"
)

// MaxQAIterations bounds the developer⇄QA retry loop. After this many failed
// QA rounds the run parks for a human instead of looping forever.
const MaxQAIterations = 3

// isTerminal reports whether s is a terminal run status (failed or completed).
// A terminal run must not be advanced by a stale/duplicate stage event.
func (s RunStatus) isTerminal() bool { return s == StatusFailed || s == StatusCompleted }

// Valid reports whether c is a known complexity class.
func (c Complexity) Valid() bool {
	switch c {
	case ComplexityTrivial, ComplexityStandard, ComplexityComplex:
		return true
	default:
		return false
	}
}

// FirstStage is the entry stage for every run.
const FirstStage = StageAnalyzing

// Run is the durable saga aggregate (one row in the runs table).
type Run struct {
	ID            string
	Status        RunStatus
	Complexity    Complexity
	CurrentStage  Stage
	Spec          string
	RepoIDs       []string
	QAIterations  int
	FailureReason *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Pipeline returns the ordered agent stages for a complexity class.
func Pipeline(c Complexity) []Stage {
	switch c {
	case ComplexityTrivial:
		return []Stage{StageAnalyzing, StageViability, StageDeveloping, StageQA}
	case ComplexityStandard:
		return []Stage{StageAnalyzing, StageViability, StageRisk, StageDesigning, StageDeveloping, StageQA}
	default: // complex (and any unknown → safest full path)
		return []Stage{StageAnalyzing, StageViability, StageRisk, StageArchitecting, StageDesigning, StageDeveloping, StageQA}
	}
}

// NextStage returns the stage following `completed` in the class pipeline.
// hasNext is false when `completed` is the final stage (delivery follows).
func NextStage(completed Stage, c Complexity) (Stage, bool, error) {
	pipe := Pipeline(c)
	for i, s := range pipe {
		if s == completed {
			if i == len(pipe)-1 {
				return "", false, nil
			}
			return pipe[i+1], true, nil
		}
	}
	return "", false, fmt.Errorf("stage %q not in %s pipeline", completed, c)
}

// RoleForStage maps a stage to its agent role for stage.requested events.
func RoleForStage(s Stage) string {
	switch s {
	case StageAnalyzing:
		return "requirement-analyzer"
	case StageViability:
		return "viability-checker"
	case StageRisk:
		return "risk-analyzer"
	case StageArchitecting:
		return "system-architect"
	case StageDesigning:
		return "system-designer"
	case StageDeveloping:
		return "developer"
	case StageQA:
		return "qa"
	default:
		return ""
	}
}
