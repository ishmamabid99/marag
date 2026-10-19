package linear

import (
	"encoding/json"
	"fmt"
)

// Event is a normalized Linear webhook event.
type Event struct {
	Action      string
	Type        string
	IssueID     string
	SourceID    string
	Title       string
	Description string
	CommentBody string
	// Actor identifies the Linear user who performed the action (for
	// authorization). Any field may be empty depending on the payload.
	ActorID    string
	ActorName  string
	ActorEmail string
}

// ParseWebhook normalizes a Linear Issue/Comment webhook payload.
func ParseWebhook(body []byte) (Event, error) {
	type person struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	var raw struct {
		Action string `json:"action"`
		Type   string `json:"type"`
		Actor  person `json:"actor"` // top-level: who performed the action
		Data   struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Body        string `json:"body"` // Comment
			User        person `json:"user"` // comment author (fallback for actor)
			Issue       struct {
				ID          string `json:"id"`
				Title       string `json:"title"`
				Description string `json:"description"`
			} `json:"issue"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Event{}, fmt.Errorf("linear: parse webhook: %w", err)
	}
	// Prefer the top-level actor; fall back to the comment's author.
	actor := raw.Actor
	if actor.ID == "" && actor.Email == "" && actor.Name == "" {
		actor = raw.Data.User
	}
	e := Event{
		Action: raw.Action, Type: raw.Type, SourceID: raw.Data.ID,
		ActorID: actor.ID, ActorName: actor.Name, ActorEmail: actor.Email,
	}
	switch raw.Type {
	case "Comment":
		e.IssueID = raw.Data.Issue.ID
		e.Title = raw.Data.Issue.Title
		e.Description = raw.Data.Issue.Description
		e.CommentBody = raw.Data.Body
	default: // "Issue" and others carry data directly
		e.IssueID = raw.Data.ID
		e.Title = raw.Data.Title
		e.Description = raw.Data.Description
	}
	return e, nil
}
