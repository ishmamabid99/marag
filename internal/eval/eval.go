// Package eval is a golden-fixture regression harness for the JSON-artifact
// agent roles: replay a recorded LLM reply through a role handler and assert
// the produced artifact meets expectations.
package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/fin/multi-agent-rag/internal/worker"
	"github.com/fin/multi-agent-rag/pkg/llm"
)

// Case is one eval fixture.
type Case struct {
	Role           string                     `json:"role"`
	Name           string                     `json:"name"`
	Spec           string                     `json:"spec"`
	RepoIDs        []string                   `json:"repo_ids"`
	PriorArtifacts map[string]json.RawMessage `json:"prior_artifacts"`
	Reply          string                     `json:"reply"`
	ExpectError    bool                       `json:"expect_error"`
	ExpectFields   map[string]any             `json:"expect_fields"`
}

// Result is the outcome of running a Case.
type Result struct {
	Name   string
	Role   string
	Passed bool
	Detail string
}

func handlerFor(role string, sm *worker.SessionManager) (worker.StageHandler, error) {
	switch role {
	case "requirement-analyzer":
		return worker.NewRequirementAnalyzer(sm), nil
	case "viability-checker":
		return worker.NewViabilityChecker(sm), nil
	case "risk-analyzer":
		return worker.NewRiskAnalyzer(sm), nil
	case "system-architect":
		return worker.NewSystemArchitect(sm), nil
	case "system-designer":
		return worker.NewSystemDesigner(sm), nil
	case "qa":
		return worker.NewQA(sm), nil
	default:
		return nil, fmt.Errorf("eval: unknown role %q", role)
	}
}

// Run executes one case: replay the reply through the role, check expectations.
func Run(c Case) Result {
	res := Result{Name: c.Name, Role: c.Role}
	sm := worker.NewSessionManager(llm.NewFake(func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{Text: c.Reply}, nil
	}), worker.SessionConfig{})
	h, err := handlerFor(c.Role, sm)
	if err != nil {
		res.Detail = err.Error()
		return res
	}
	prior := map[string][]byte{}
	for k, v := range c.PriorArtifacts {
		prior[k] = []byte(v)
	}
	out, err := h.Handle(context.Background(), worker.StageInput{
		RunID: "eval", Stage: c.Role, Spec: c.Spec, RepoIDs: c.RepoIDs, PriorArtifacts: prior,
	})
	if c.ExpectError {
		res.Passed = err != nil
		if !res.Passed {
			res.Detail = "expected an error, got none"
		}
		return res
	}
	if err != nil {
		res.Detail = fmt.Sprintf("unexpected error: %v", err)
		return res
	}
	var art map[string]any
	if uerr := json.Unmarshal(out.ArtifactJSON, &art); uerr != nil {
		res.Detail = fmt.Sprintf("artifact not JSON object: %v", uerr)
		return res
	}
	for k, want := range c.ExpectFields {
		got, ok := art[k]
		if !ok {
			res.Detail = fmt.Sprintf("missing field %q", k)
			return res
		}
		if !reflect.DeepEqual(got, want) {
			res.Detail = fmt.Sprintf("field %q = %v, want %v", k, got, want)
			return res
		}
	}
	res.Passed = true
	return res
}

// LoadCases reads all *.json fixtures under dir (recursively). Each file is one Case.
func LoadCases(dir string) ([]Case, error) {
	var cases []Case
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return fmt.Errorf("eval: read %s: %w", path, rerr)
		}
		var c Case
		if uerr := json.Unmarshal(data, &c); uerr != nil {
			return fmt.Errorf("eval: parse %s: %w", path, uerr)
		}
		if c.Name == "" {
			c.Name = filepath.Base(path)
		}
		cases = append(cases, c)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return cases, nil
}
