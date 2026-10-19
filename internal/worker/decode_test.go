package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/fin/multi-agent-rag/pkg/llm"
)

func TestDecodeReplyRetriesThenSucceeds(t *testing.T) {
	var n int
	be := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		n++
		if n == 1 {
			return llm.Response{Text: "not json", Usage: llm.Usage{OutputTokens: 1}}, nil
		}
		return llm.Response{Text: `{"v":"ok"}`, Usage: llm.Usage{OutputTokens: 2}}, nil
	})
	sm := NewSessionManager(be, SessionConfig{})
	type payload struct {
		V string `json:"v"`
	}
	parse := func(s string) (payload, error) {
		var v payload
		if err := json.Unmarshal([]byte(extractJSONObject(s)), &v); err != nil {
			return v, err
		}
		if v.V == "" {
			return v, errors.New("missing v")
		}
		return v, nil
	}
	got, usage, err := decodeReply(context.Background(), sm, llm.Request{}, parse)
	if err != nil || got.V != "ok" {
		t.Fatalf("got %+v err %v", got, err)
	}
	if n != 2 {
		t.Fatalf("expected 2 attempts, got %d", n)
	}
	if usage.OutputTokens != 3 {
		t.Fatalf("usage should accumulate across retries: %d", usage.OutputTokens)
	}
}

func TestDecodeReplyFailsAfterMax(t *testing.T) {
	be := llm.NewFake(func(_ context.Context, _ llm.Request) (llm.Response, error) {
		return llm.Response{Text: "never valid"}, nil
	})
	sm := NewSessionManager(be, SessionConfig{})
	parse := func(string) (struct{}, error) { return struct{}{}, errors.New("bad") }
	if _, _, err := decodeReply(context.Background(), sm, llm.Request{}, parse); err == nil {
		t.Fatal("expected failure after max attempts")
	}
}
