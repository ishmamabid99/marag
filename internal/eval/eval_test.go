package eval

import "testing"

func TestRunViabilityFeasiblePasses(t *testing.T) {
	c := Case{
		Role: "viability-checker", Name: "feasible",
		Spec:         "add a config flag",
		Reply:        `{"feasible":true,"confirmed_complexity":"trivial","feasibility_rationale":"simple","risks_identified":[],"constraints":[]}`,
		ExpectFields: map[string]any{"feasible": true, "confirmed_complexity": "trivial"},
	}
	r := Run(c)
	if !r.Passed {
		t.Fatalf("expected pass: %s", r.Detail)
	}
}

func TestRunExpectedError(t *testing.T) {
	c := Case{
		Role: "requirement-analyzer", Name: "bad-complexity",
		Spec:        "x",
		Reply:       `{"preliminary_complexity":"nonsense"}`,
		ExpectError: true,
	}
	r := Run(c)
	if !r.Passed {
		t.Fatalf("expected pass (error was expected): %s", r.Detail)
	}
}

func TestRunFieldMismatchFails(t *testing.T) {
	c := Case{
		Role: "viability-checker", Name: "mismatch",
		Spec:         "x",
		Reply:        `{"feasible":true,"confirmed_complexity":"complex"}`,
		ExpectFields: map[string]any{"confirmed_complexity": "trivial"}, // expect trivial, got complex
	}
	r := Run(c)
	if r.Passed {
		t.Fatalf("expected fail on field mismatch")
	}
}

func TestRunUnknownRole(t *testing.T) {
	r := Run(Case{Role: "nope", Name: "x", Reply: "{}"})
	if r.Passed {
		t.Fatalf("unknown role should fail")
	}
}
