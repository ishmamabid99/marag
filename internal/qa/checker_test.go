package qa

import (
	"context"
	"testing"

	"github.com/fin/multi-agent-rag/internal/sandbox"
)

// seedModule writes a minimal Go module into the sandbox. calc + optional test.
func seedModule(t *testing.T, sb sandbox.Sandbox, calcGo, calcTestGo string) {
	t.Helper()
	if err := sb.WriteFile("go.mod", []byte("module qacheck\n\ngo 1.21\n")); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := sb.WriteFile("calc.go", []byte(calcGo)); err != nil {
		t.Fatalf("write calc.go: %v", err)
	}
	if calcTestGo != "" {
		if err := sb.WriteFile("calc_test.go", []byte(calcTestGo)); err != nil {
			t.Fatalf("write calc_test.go: %v", err)
		}
	}
}

func newSandbox(t *testing.T) sandbox.Sandbox {
	t.Helper()
	sb, err := sandbox.NewLocal(context.Background())
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	t.Cleanup(func() { _ = sb.Close() })
	return sb
}

func TestRunChecksAllPass(t *testing.T) {
	sb := newSandbox(t)
	seedModule(t, sb,
		"package qacheck\n\nfunc Add(a, b int) int { return a + b }\n",
		"package qacheck\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n",
	)
	res, err := RunChecks(context.Background(), sb)
	if err != nil {
		t.Fatalf("RunChecks: %v", err)
	}
	if !res.AllPassed || !res.Build.Passed || !res.Vet.Passed || !res.Test.Passed {
		t.Fatalf("res = %+v", res)
	}
	if len(res.FailingTests) != 0 {
		t.Fatalf("FailingTests = %v, want none", res.FailingTests)
	}
}

func TestRunChecksVetFailureFailsOverall(t *testing.T) {
	sb := newSandbox(t)
	// Compiles and the test passes, but `go vet` flags unreachable code, so the
	// run must fail overall (AllPassed=false) even though Test passed. The
	// `unreachable` analyzer is in `go vet` but NOT in `go test`'s default vet
	// subset, so `go test` stays green here — isolating the vet-only path.
	seedModule(t, sb,
		"package qacheck\n\nfunc Add(a, b int) int { return a + b }\n\nfunc dead() int {\n\treturn 1\n\treturn 2\n}\n",
		"package qacheck\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n",
	)
	res, err := RunChecks(context.Background(), sb)
	if err != nil {
		t.Fatalf("RunChecks: %v", err)
	}
	if !res.Build.Passed {
		t.Fatalf("Build should pass (it compiles): %+v", res.Build)
	}
	if res.Vet.Passed {
		t.Fatalf("Vet.Passed = true, want false (Printf mismatch)")
	}
	if !res.Test.Passed {
		t.Fatalf("Test should pass: %+v", res.Test)
	}
	if res.AllPassed {
		t.Fatalf("AllPassed = true, want false (vet failed)")
	}
}

func TestRunChecksBuildFailureShortCircuits(t *testing.T) {
	sb := newSandbox(t)
	// Syntax error → build fails; vet/test must be skipped.
	seedModule(t, sb, "package qacheck\n\nfunc Add(a, b int) int { return a + \n", "")
	res, err := RunChecks(context.Background(), sb)
	if err != nil {
		t.Fatalf("RunChecks: %v", err)
	}
	if res.AllPassed {
		t.Fatalf("AllPassed = true, want false")
	}
	if res.Build.Passed {
		t.Fatalf("Build.Passed = true, want false")
	}
	// vet/test were skipped, so they retain the zero value (not passed).
	if res.Vet.Passed || res.Test.Passed {
		t.Fatalf("vet/test should be skipped: vet=%+v test=%+v", res.Vet, res.Test)
	}
	if res.Build.Output == "" {
		t.Fatalf("Build.Output should capture the compiler error")
	}
}

func TestRunChecksTestFailureParsesNames(t *testing.T) {
	sb := newSandbox(t)
	seedModule(t, sb,
		"package qacheck\n\nfunc Add(a, b int) int { return a - b }\n", // wrong impl
		"package qacheck\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatalf(\"got %d\", Add(2, 3))\n\t}\n}\n",
	)
	res, err := RunChecks(context.Background(), sb)
	if err != nil {
		t.Fatalf("RunChecks: %v", err)
	}
	if res.AllPassed {
		t.Fatalf("AllPassed = true, want false")
	}
	if !res.Build.Passed {
		t.Fatalf("Build should pass (it compiles): %+v", res.Build)
	}
	if res.Test.Passed {
		t.Fatalf("Test.Passed = true, want false")
	}
	found := false
	for _, n := range res.FailingTests {
		if n == "TestAdd" {
			found = true
		}
	}
	if !found {
		t.Fatalf("FailingTests = %v, want to contain TestAdd", res.FailingTests)
	}
}

func TestLooksLikeEnvFailure(t *testing.T) {
	if !looksLikeEnvFailure("panic: dial tcp [::1]:3306: connect: connection refused") {
		t.Fatal("should detect a connection-refused panic as an env failure")
	}
	if looksLikeEnvFailure("--- FAIL: TestAdd\n  got 6 want 5") {
		t.Fatal("a genuine assertion failure must not be flagged as env")
	}
}

func TestRunChecksDetectsEnvUnavailable(t *testing.T) {
	sb := newSandbox(t)
	// A test that dials a port nothing listens on → connection refused, but the
	// code compiles + vets cleanly: this is an environment gap, not a defect.
	seedModule(t, sb,
		"package qacheck\n\nfunc Add(a, b int) int { return a + b }\n",
		"package qacheck\n\nimport (\n\t\"net\"\n\t\"testing\"\n)\n\nfunc TestNeedsService(t *testing.T) {\n\tc, err := net.Dial(\"tcp\", \"127.0.0.1:1\")\n\tif err != nil {\n\t\tt.Fatalf(\"%v\", err)\n\t}\n\t_ = c.Close()\n}\n",
	)
	r, err := RunChecks(context.Background(), sb)
	if err != nil {
		t.Fatalf("RunChecks: %v", err)
	}
	if !r.Build.Passed || !r.Vet.Passed {
		t.Fatalf("build+vet should pass: build=%+v vet=%+v", r.Build, r.Vet)
	}
	if r.Test.Passed {
		t.Fatal("test should fail (connection refused)")
	}
	if !r.EnvUnavailable {
		t.Fatalf("should flag EnvUnavailable; test output:\n%s", r.Test.Output)
	}
}
