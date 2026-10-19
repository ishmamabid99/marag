package worker

// RoleParams are the per-role LLM knobs an operator can tune: which model, the
// output-token cap, and whether adaptive "thinking" (higher effort) is on.
// Structured-output and tool wiring stay in code (they're tied to each role's
// contract), but these three are config-tunable.
type RoleParams struct {
	Model     string
	MaxTokens int
	Thinking  bool
}

// Role keys used in the roster (and in the config `roles` map).
const (
	RoleRequirementAnalyzer = "requirement-analyzer"
	RoleViabilityChecker    = "viability-checker"
	RoleRiskAnalyzer        = "risk-analyzer"
	RoleSystemArchitect     = "system-architect"
	RoleSystemDesigner      = "system-designer"
	RoleQA                  = "qa"
	RoleDeveloper           = "developer"            // tool-loop draft
	RoleDeveloperEscalation = "developer-escalation" // more-capable backup
	RoleDeveloperCheap      = "developer-cheap"      // cheapest, for trivial changes
)

// DefaultRoster returns the built-in per-role params (formerly Go constants +
// literals). Operators override any subset via the config `roles` map; unset
// fields fall back to these.
func DefaultRoster() map[string]RoleParams {
	return map[string]RoleParams{
		RoleRequirementAnalyzer: {Model: reqAnalyzerModel, MaxTokens: 1000, Thinking: false},
		RoleViabilityChecker:    {Model: viabilityModel, MaxTokens: 2000, Thinking: false},
		RoleRiskAnalyzer:        {Model: riskModel, MaxTokens: 3000, Thinking: true},
		RoleSystemArchitect:     {Model: architectModel, MaxTokens: 8000, Thinking: true},
		RoleSystemDesigner:      {Model: designerModel, MaxTokens: 8000, Thinking: true},
		RoleQA:                  {Model: qaModel, MaxTokens: 1500, Thinking: false},
		RoleDeveloper:           {Model: "claude-sonnet-5", MaxTokens: 8000},
		RoleDeveloperEscalation: {Model: "claude-opus-4-8", MaxTokens: 8000},
		RoleDeveloperCheap:      {Model: "claude-haiku-4-5", MaxTokens: 8000},
	}
}

// paramsFor returns the default params for a role (zero value if unknown).
func paramsFor(role string) RoleParams { return DefaultRoster()[role] }

// DefaultRoleConcurrency returns the built-in per-role in-flight caps (M3).
// Rationale: the reasoning roles are network-bound (waiting on the LLM) and use
// little local CPU/memory, so several run at once to raise throughput. The
// developer and QA stages drive the local sandbox (git clone, `go build`/`go
// test`) and, for the developer, real per-call LLM cost — so they're held low.
// Operators override any subset via config `worker.role_concurrency`; a role
// absent from the effective map runs unbounded.
func DefaultRoleConcurrency() map[string]int {
	return map[string]int{
		RoleRequirementAnalyzer: 4,
		RoleViabilityChecker:    4,
		RoleRiskAnalyzer:        4,
		RoleSystemArchitect:     4,
		RoleSystemDesigner:      4,
		RoleDeveloper:           1, // sandbox + memory + per-call $$$
		RoleQA:                  2, // `go test` is CPU/memory heavy
	}
}
