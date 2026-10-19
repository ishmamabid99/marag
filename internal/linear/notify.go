package linear

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// OpenedPR is one PR opened for a run (from run.pr.opened).
type OpenedPR struct {
	RepoID string
	URL    string
}

// FailedRepo is a repo that failed delivery (from run.pr.opened).
type FailedRepo struct {
	RepoID string
	Error  string
}

// Notifier posts PR-links-back and gate comments to the originating Linear issue.
type Notifier struct {
	store     RunStore
	client    Client
	artifacts GateArtifactReader // optional; enriches gate notices with stage findings
}

// NewNotifier wires the notifier.
func NewNotifier(store RunStore, client Client) *Notifier {
	return &Notifier{store: store, client: client}
}

// WithArtifacts enables enriching gate notifications with the gating stage's
// detailed findings (blast radius, breaking risks) read from the run artifact.
func (n *Notifier) WithArtifacts(r GateArtifactReader) *Notifier {
	n.artifacts = r
	return n
}

// Notify posts the opened PRs (and any failures) as a comment on the run's
// Linear issue. An unknown run is a no-op (nil error).
func (n *Notifier) Notify(ctx context.Context, runID string, prs []OpenedPR, failed []FailedRepo) error {
	issue, ok, err := n.store.IssueForRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("linear: notify lookup: %w", err)
	}
	if !ok {
		return nil // not a Linear-originated run
	}
	var b strings.Builder
	if len(prs) > 0 {
		b.WriteString("Opened pull request(s):\n")
		for _, pr := range prs {
			fmt.Fprintf(&b, "- %s: %s\n", pr.RepoID, pr.URL)
		}
	} else {
		b.WriteString("No pull requests were opened.\n")
	}
	if len(failed) > 0 {
		b.WriteString("\nRepositories that could not be completed:\n")
		for _, f := range failed {
			fmt.Fprintf(&b, "- %s: %s\n", f.RepoID, f.Error)
		}
	}
	if err := n.client.PostComment(ctx, issue, b.String()); err != nil {
		return fmt.Errorf("linear: notify comment: %w", err)
	}
	return nil
}

// NotifyGate posts a gate notification on the run's Linear issue: the run is
// paused awaiting human approval (e.g. the analyzer proposed additional repos).
// An unknown run is a no-op (nil error) — not a Linear-originated run.
func (n *Notifier) NotifyGate(ctx context.Context, runID, stage, reason string) error {
	issue, ok, err := n.store.IssueForRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("linear: notify gate lookup: %w", err)
	}
	if !ok {
		return nil // not a Linear-originated run
	}
	if err := n.client.PostComment(ctx, issue, n.gateBody(ctx, runID, stage, reason)); err != nil {
		return fmt.Errorf("linear: notify gate comment: %w", err)
	}
	return nil
}

// NotifyFailure posts a terminal-failure notice on the run's Linear issue,
// including the stage and reason (which carries the developer's captured
// build/test errors). An unknown run is a no-op (not Linear-originated).
func (n *Notifier) NotifyFailure(ctx context.Context, runID, stage, reason string) error {
	issue, ok, err := n.store.IssueForRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("linear: notify failure lookup: %w", err)
	}
	if !ok {
		return nil
	}
	body := fmt.Sprintf("❌ Run `%s` failed at the **%s** stage.\n\n```\n%s\n```", runID, stage, strings.TrimSpace(reason))
	if err := n.client.PostComment(ctx, issue, body); err != nil {
		return fmt.Errorf("linear: notify failure comment: %w", err)
	}
	return nil
}

// gateBody composes the gate notification, enriched with the gating stage's
// artifact findings (blast radius, breaking risks) when an artifact reader is
// configured and the artifact carries them. Falls back to the reason line.
func (n *Notifier) gateBody(ctx context.Context, runID, stage, reason string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "⏸️ Run `%s` is paused for approval at the **%s** stage.\n\n", runID, stage)

	if n.artifacts != nil {
		if raw, ok, err := n.artifacts.Artifact(ctx, runID, stage); err == nil && ok {
			var d struct {
				GateReason    string   `json:"gate_reason"`
				BlastRadius   string   `json:"blast_radius"`
				BreakingRisks []string `json:"breaking_risks"`
			}
			if json.Unmarshal(raw, &d) == nil {
				if d.GateReason != "" {
					reason = d.GateReason // prefer the artifact's rationale
				}
				if d.BlastRadius != "" {
					fmt.Fprintf(&b, "**Blast radius:** %s\n\n", d.BlastRadius)
				}
				if reason != "" {
					fmt.Fprintf(&b, "%s\n", reason)
				}
				for _, r := range d.BreakingRisks {
					fmt.Fprintf(&b, "- %s\n", r)
				}
				b.WriteString("\nReply `@marag approve` to proceed or `@marag reject` to stop.")
				return b.String()
			}
		}
	}

	if reason != "" {
		fmt.Fprintf(&b, "%s\n\n", reason)
	}
	b.WriteString("Reply `@marag approve` to proceed or `@marag reject` to stop.")
	return b.String()
}
