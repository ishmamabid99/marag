package worker

import "testing"

// Anthropic structured outputs require every object schema to explicitly set
// additionalProperties:false, else the API 400s. Lock that in for the role
// output schemas (regression guard — the fake LLM in other tests can't catch it).
func TestOutputSchemasSetAdditionalPropertiesFalse(t *testing.T) {
	for name, s := range map[string]map[string]any{
		"reqAnalyzer": reqAnalyzerSchema,
		"viability":   viabilitySchema,
		"qa":          qaSchema,
	} {
		if v, ok := s["additionalProperties"]; !ok || v != false {
			t.Fatalf("%s schema must set additionalProperties:false (got %v, ok=%v)", name, v, ok)
		}
	}
}
