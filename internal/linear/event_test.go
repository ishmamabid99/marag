package linear

import "testing"

func TestParseWebhookIssue(t *testing.T) {
	body := []byte(`{"action":"create","type":"Issue","data":{"id":"iss-1","title":"Add currency","description":"Support EUR. Repos: payments, checkout"}}`)
	e, err := ParseWebhook(body)
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	if e.Type != "Issue" || e.IssueID != "iss-1" || e.Title != "Add currency" {
		t.Fatalf("event = %+v", e)
	}
	if e.Description == "" {
		t.Fatalf("missing description")
	}
	if e.SourceID != "iss-1" {
		t.Fatalf("SourceID = %q, want iss-1", e.SourceID)
	}
}

func TestParseWebhookComment(t *testing.T) {
	body := []byte(`{"action":"create","type":"Comment","data":{"id":"cmt-9","body":"@marag please implement","issue":{"id":"iss-2","title":"T","description":"D"}}}`)
	e, err := ParseWebhook(body)
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	if e.Type != "Comment" || e.IssueID != "iss-2" || e.CommentBody != "@marag please implement" || e.Title != "T" {
		t.Fatalf("event = %+v", e)
	}
	if e.SourceID != "cmt-9" {
		t.Fatalf("SourceID = %q, want cmt-9", e.SourceID)
	}
}

func TestParseWebhookBadJSON(t *testing.T) {
	if _, err := ParseWebhook([]byte(`not json`)); err == nil {
		t.Fatalf("expected error for bad json")
	}
}
