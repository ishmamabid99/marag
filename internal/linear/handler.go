package linear

import (
	"context"
	"fmt"
	"log"
	"net/mail"
	"strings"
)

// Submitter starts a run, keyed for idempotency (satisfied by *gateway.Gateway).
type Submitter interface {
	SubmitRunKeyed(ctx context.Context, key, spec string, repoIDs []string) (string, error)
}

// Resumer approves/rejects a run parked at a human gate (satisfied by
// *gateway.Gateway). Resuming a non-gated run is a safe no-op.
type Resumer interface {
	ResumeGate(ctx context.Context, runID string, approved bool) error
}

// Handler processes a Linear webhook: verify → parse → (if triggered) submit + ack.
type Handler struct {
	secret  string
	trigger string
	sub     Submitter
	client  Client
	store   RunStore
	resume  Resumer
	allowed map[string]bool // normalized actor allowlist; empty = allow all
}

// NewHandler wires the handler.
func NewHandler(secret, trigger string, sub Submitter, client Client) *Handler {
	if trigger == "" {
		trigger = "@marag"
	}
	return &Handler{secret: secret, trigger: trigger, sub: sub, client: client}
}

// WithRunStore opts the handler into persisting run_id -> issue_id mappings
// after a successful submit. Without it, Handler behaves as before.
func (h *Handler) WithRunStore(s RunStore) *Handler {
	h.store = s
	return h
}

// WithResumer enables in-thread gate approval: a triggering comment that says
// "approve" or "reject" (and maps to a prior run on the issue) resumes that run
// instead of starting a new one. Requires a RunStore.
func (h *Handler) WithResumer(r Resumer) *Handler {
	h.resume = r
	return h
}

// WithAllowedActors restricts who may trigger/approve via the trigger token.
// Entries are matched against the webhook actor's email (case-insensitive),
// Linear user id, or name. Email-shaped entries are validated; malformed ones
// are dropped with a warning. An empty/all-invalid list allows everyone.
func (h *Handler) WithAllowedActors(actors []string) *Handler {
	h.allowed = map[string]bool{}
	for _, a := range actors {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if strings.Contains(a, "@") {
			if _, err := mail.ParseAddress(a); err != nil {
				log.Printf("linear-gateway: ignoring malformed allowed-actor email %q: %v", a, err)
				continue
			}
			h.allowed[strings.ToLower(a)] = true
		} else {
			h.allowed[a] = true // Linear user id or name (exact match)
		}
	}
	return h
}

// actorAllowed reports whether the event's actor may trigger/approve. An empty
// allowlist permits everyone (back-compat).
func (h *Handler) actorAllowed(e Event) bool {
	if len(h.allowed) == 0 {
		return true
	}
	if e.ActorID != "" && h.allowed[e.ActorID] {
		return true
	}
	if e.ActorName != "" && h.allowed[e.ActorName] {
		return true
	}
	if e.ActorEmail != "" && h.allowed[strings.ToLower(e.ActorEmail)] {
		return true
	}
	return false
}

// Handle verifies the signature, and if the event contains the trigger token,
// submits a run and posts an acknowledgement comment. triggered is false (nil
// error) for a valid-but-non-triggering event.
func (h *Handler) Handle(ctx context.Context, signatureHeader string, body []byte) (bool, string, error) {
	if err := VerifySignature(body, signatureHeader, h.secret); err != nil {
		return false, "", err
	}
	evt, err := ParseWebhook(body)
	if err != nil {
		return false, "", err
	}
	if evt.Action != "create" {
		return false, "", nil
	}
	text := evt.CommentBody + "\n" + evt.Title + "\n" + evt.Description
	lower := strings.ToLower(text)
	if !strings.Contains(lower, strings.ToLower(h.trigger)) {
		return false, "", nil
	}

	// Authorization: only allowlisted actors may trigger or approve. Logged
	// (allowed and blocked) so operators can see who's triggering and populate
	// the allowlist. An empty allowlist permits everyone (back-compat).
	if !h.actorAllowed(evt) {
		log.Printf("linear-gateway: BLOCKED trigger from unauthorized actor id=%q name=%q email=%q on issue %s",
			evt.ActorID, evt.ActorName, evt.ActorEmail, evt.IssueID)
		return false, "", nil
	}
	log.Printf("linear-gateway: trigger from actor id=%q name=%q email=%q on issue %s",
		evt.ActorID, evt.ActorName, evt.ActorEmail, evt.IssueID)

	// In-thread gate control: a triggering comment that says exactly one of
	// "approve"/"reject" and maps to a prior run on this issue resumes that run
	// instead of starting a new one. (Resuming a non-gated run is a safe no-op,
	// so a false positive can't corrupt state — it just posts a benign ack.)
	if h.resume != nil && h.store != nil {
		approve := strings.Contains(lower, "approve")
		reject := strings.Contains(lower, "reject")
		if approve != reject {
			runID, ok, err := h.store.RunForIssue(ctx, evt.IssueID)
			if err != nil {
				return false, "", fmt.Errorf("linear: run for issue: %w", err)
			}
			if ok {
				if err := h.resume.ResumeGate(ctx, runID, approve); err != nil {
					return false, "", fmt.Errorf("linear: resume gate: %w", err)
				}
				verb := "Approved"
				if !approve {
					verb = "Rejected"
				}
				ack := fmt.Sprintf("%s run `%s`.", verb, runID)
				if err := h.client.PostComment(ctx, evt.IssueID, ack); err != nil {
					return true, runID, fmt.Errorf("linear: ack comment: %w", err)
				}
				return true, runID, nil
			}
		}
	}

	spec := buildSpec(evt)
	if strings.TrimSpace(spec) == "" {
		return false, "", fmt.Errorf("linear: empty spec from issue %s", evt.IssueID)
	}
	runID, err := h.sub.SubmitRunKeyed(ctx, evt.SourceID, spec, extractRepoIDs(text))
	if err != nil {
		return false, "", fmt.Errorf("linear: submit run: %w", err)
	}
	if h.store != nil {
		// Best-effort: the run already started; a mapping-save failure only
		// loses the later PR-links-back comment, so it must not fail the trigger.
		_ = h.store.SaveMapping(ctx, runID, evt.IssueID)
	}
	ack := fmt.Sprintf("Started run `%s`. I'll implement this and open PR(s).", runID)
	if err := h.client.PostComment(ctx, evt.IssueID, ack); err != nil {
		// The run is already submitted; report the ack failure but keep the run id.
		return true, runID, fmt.Errorf("linear: ack comment: %w", err)
	}
	return true, runID, nil
}

func buildSpec(e Event) string {
	var b strings.Builder
	b.WriteString(e.Title)
	if e.Description != "" {
		b.WriteString("\n\n")
		b.WriteString(e.Description)
	}
	if e.CommentBody != "" {
		b.WriteString("\n\nInstruction: ")
		b.WriteString(e.CommentBody)
	}
	return strings.TrimSpace(b.String())
}

// extractRepoIDs parses a "Repos: a, b, c" hint (case-insensitive), which may
// appear anywhere in the text (start of a line or mid-sentence).
func extractRepoIDs(text string) []string {
	const key = "repos:"
	idx := strings.Index(strings.ToLower(text), key)
	if idx == -1 {
		return nil
	}
	rest := text[idx+len(key):]
	if nl := strings.IndexByte(rest, '\n'); nl != -1 {
		rest = rest[:nl]
	}
	var ids []string
	for _, p := range strings.Split(rest, ",") {
		if id := strings.TrimSpace(p); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}
