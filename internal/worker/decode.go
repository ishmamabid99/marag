package worker

import (
	"context"
	"fmt"

	"github.com/fin/multi-agent-rag/pkg/llm"
)

const maxDecodeAttempts = 3

// decodeReply completes req and parses the reply with parse, re-prompting the
// model on a parse/validation error up to maxDecodeAttempts before giving up.
// Usage accumulates across attempts. This is the contract-validation gate: a
// role's reply is only accepted once parse (unmarshal + validation) succeeds.
func decodeReply[T any](ctx context.Context, sm *SessionManager, req llm.Request, parse func(text string) (T, error)) (T, llm.Usage, error) {
	var zero T
	var usage llm.Usage
	var lastErr error
	for attempt := 1; attempt <= maxDecodeAttempts; attempt++ {
		resp, err := sm.Complete(ctx, req)
		if err != nil {
			return zero, usage, err
		}
		usage.InputTokens += resp.Usage.InputTokens
		usage.OutputTokens += resp.Usage.OutputTokens
		v, perr := parse(resp.Text)
		if perr == nil {
			return v, usage, nil
		}
		lastErr = perr
		req.Messages = append(req.Messages,
			llm.Message{Role: "assistant", Content: resp.Text},
			llm.Message{Role: "user", Content: fmt.Sprintf("Your reply could not be accepted: %v. Reply again with ONLY the JSON object required by the contract — no prose, no code fences, all required fields present.", perr)})
	}
	return zero, usage, fmt.Errorf("invalid reply after %d attempts: %w", maxDecodeAttempts, lastErr)
}
