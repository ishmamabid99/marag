package managedagent

import (
	"context"
	"testing"
)

func TestFakeMAClientRecords(t *testing.T) {
	f := NewFakeMAClient(func(_ context.Context, req MASession) (MAResult, error) {
		return MAResult{Branch: req.Branch, HeadCommit: "sha1", TestsPassed: true, Summary: "done"}, nil
	})
	res, err := f.RunDeveloperSession(context.Background(), MASession{RepoID: "r", Branch: "marag/run-1"})
	if err != nil {
		t.Fatalf("RunDeveloperSession: %v", err)
	}
	if !res.TestsPassed || res.Branch != "marag/run-1" {
		t.Fatalf("res = %+v", res)
	}
	if len(f.Calls()) != 1 || f.Calls()[0].RepoID != "r" {
		t.Fatalf("calls = %+v", f.Calls())
	}
}
