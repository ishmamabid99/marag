package orchestrator

import "testing"

func TestPipelines(t *testing.T) {
	tests := []struct {
		c    Complexity
		want []Stage
	}{
		{ComplexityTrivial, []Stage{StageAnalyzing, StageViability, StageDeveloping, StageQA}},
		{ComplexityStandard, []Stage{StageAnalyzing, StageViability, StageRisk, StageDesigning, StageDeveloping, StageQA}},
		{ComplexityComplex, []Stage{StageAnalyzing, StageViability, StageRisk, StageArchitecting, StageDesigning, StageDeveloping, StageQA}},
	}
	for _, tt := range tests {
		got := Pipeline(tt.c)
		if len(got) != len(tt.want) {
			t.Fatalf("%s: got %v, want %v", tt.c, got, tt.want)
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Fatalf("%s[%d] = %s, want %s", tt.c, i, got[i], tt.want[i])
			}
		}
		if got[0] != FirstStage {
			t.Fatalf("%s first stage = %s, want %s", tt.c, got[0], FirstStage)
		}
	}
}

func TestNextStage(t *testing.T) {
	// complex: analyzing → viability → risk → architecting → designing → developing → qa → (done)
	next, hasNext, err := NextStage(StageAnalyzing, ComplexityComplex)
	if err != nil || !hasNext || next != StageViability {
		t.Fatalf("analyzing/complex → %s,%v,%v", next, hasNext, err)
	}
	// trivial skips risk/architecting/designing: viability → developing
	next, hasNext, err = NextStage(StageViability, ComplexityTrivial)
	if err != nil || !hasNext || next != StageDeveloping {
		t.Fatalf("viability/trivial → %s,%v,%v", next, hasNext, err)
	}
	// standard skips architecting: risk → designing
	next, _, _ = NextStage(StageRisk, ComplexityStandard)
	if next != StageDesigning {
		t.Fatalf("risk/standard → %s, want designing", next)
	}
	// qa is last: hasNext=false
	_, hasNext, err = NextStage(StageQA, ComplexityComplex)
	if err != nil || hasNext {
		t.Fatalf("qa/complex → hasNext=%v err=%v, want false/nil", hasNext, err)
	}
	// a stage not in the trivial pipeline (risk) errors
	if _, _, err := NextStage(StageRisk, ComplexityTrivial); err == nil {
		t.Fatal("risk/trivial should error (not in pipeline)")
	}
}

func TestRoleForStage(t *testing.T) {
	cases := map[Stage]string{
		StageAnalyzing: "requirement-analyzer", StageViability: "viability-checker",
		StageRisk: "risk-analyzer", StageArchitecting: "system-architect",
		StageDesigning: "system-designer", StageDeveloping: "developer", StageQA: "qa",
	}
	for s, want := range cases {
		if got := RoleForStage(s); got != want {
			t.Errorf("RoleForStage(%s) = %q, want %q", s, got, want)
		}
	}
}

func TestComplexityValid(t *testing.T) {
	for _, c := range []Complexity{ComplexityTrivial, ComplexityStandard, ComplexityComplex} {
		if !c.Valid() {
			t.Errorf("%s should be valid", c)
		}
	}
	if Complexity("huge").Valid() {
		t.Error("huge should be invalid")
	}
}
