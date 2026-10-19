// Command llm-probe validates the Claude backend end-to-end through the
// platform's own llm.Backend — especially the Vertex path — before running the
// full pipeline. It runs three checks (plain / adaptive-thinking / structured
// output) and reports which succeed, so you know how to set
// anthropic.disable_thinking / disable_structured_output.
//
// Vertex (uses Application Default Credentials):
//
//	go run ./cmd/llm-probe -provider vertex -project ninjacoder -region global -model claude-opus-4-8
//
// Direct API (uses ANTHROPIC_API_KEY):
//
//	go run ./cmd/llm-probe -provider api -model claude-opus-4-8
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/fin/multi-agent-rag/pkg/llm"
)

func main() {
	provider := flag.String("provider", "vertex", "api | vertex")
	project := flag.String("project", "", "vertex project id")
	region := flag.String("region", "global", "vertex region (e.g. global, us-east5)")
	model := flag.String("model", "claude-opus-4-8", "model id")
	flag.Parse()

	ctx := context.Background()
	var backend llm.Backend
	switch *provider {
	case "vertex":
		if *project == "" {
			log.Fatal("vertex requires -project")
		}
		backend = llm.NewAnthropic(llm.WithVertex(ctx, *region, *project))
		fmt.Printf("probe: vertex %s/%s model=%s\n\n", *region, *project, *model)
	case "api":
		backend = llm.NewAnthropic()
		fmt.Printf("probe: direct api model=%s\n\n", *model)
	default:
		log.Fatalf("unknown provider %q", *provider)
	}

	ok := true
	// 1. Plain completion — the baseline (auth + endpoint + model).
	ok = check(ctx, backend, "plain", llm.Request{
		Model: *model, MaxTokens: 64,
		Messages: []llm.Message{{Role: "user", Content: "Reply with exactly: pong"}},
	}) && ok
	// 2. Adaptive thinking (used by risk/architect/designer).
	thinkingOK := check(ctx, backend, "thinking", llm.Request{
		Model: *model, MaxTokens: 1024, Thinking: true,
		Messages: []llm.Message{{Role: "user", Content: "What is 2+2? Answer briefly."}},
	})
	// 3. Structured output (used by analyzer/viability/qa).
	structuredOK := check(ctx, backend, "structured-output", llm.Request{
		Model: *model, MaxTokens: 128,
		Messages: []llm.Message{{Role: "user", Content: "Give me a color."}},
		OutputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"color": map[string]any{"type": "string"}},
			"required":             []string{"color"},
			"additionalProperties": false,
		},
	})

	fmt.Println("\n--- config guidance ---")
	fmt.Printf("anthropic.disable_thinking:          %v\n", !thinkingOK)
	fmt.Printf("anthropic.disable_structured_output: %v\n", !structuredOK)
	if !ok {
		fmt.Println("\nplain check failed — fix auth/model/region before anything else.")
		os.Exit(1)
	}
}

func check(ctx context.Context, b llm.Backend, name string, req llm.Request) bool {
	resp, err := b.Complete(ctx, req)
	if err != nil {
		fmt.Printf("[FAIL] %-18s %v\n", name, err)
		return false
	}
	out := resp.Text
	if len(out) > 80 {
		out = out[:80] + "…"
	}
	fmt.Printf("[ OK ] %-18s %q\n", name, out)
	return true
}
