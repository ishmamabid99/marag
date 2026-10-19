package eval_test

import (
	"testing"

	"github.com/fin/multi-agent-rag/internal/eval"
)

func TestGoldenFixtures(t *testing.T) {
	cases, err := eval.LoadCases("fixtures")
	if err != nil {
		t.Fatalf("LoadCases: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("no fixtures found")
	}
	for _, c := range cases {
		c := c
		t.Run(c.Role+"/"+c.Name, func(t *testing.T) {
			r := eval.Run(c)
			if !r.Passed {
				t.Fatalf("case failed: %s", r.Detail)
			}
		})
	}
}
