package llm

import "testing"

// Construction smoke test for the model-override + feature-toggle options.
// WithVertex is intentionally NOT exercised here: vertex.WithGoogleAuth loads
// Google ADC eagerly and panics without credentials — it's compile-verified and
// validated live with GCP access (same posture as the rest of the LLM layer).
func TestNewAnthropicOverridesAndToggles(t *testing.T) {
	a := NewAnthropic(
		WithModelOverrides(map[string]string{"claude-sonnet-5": "claude-sonnet-4-5@x"}),
		WithFeatureToggles(true, true),
	)
	if a.modelOverrides["claude-sonnet-5"] != "claude-sonnet-4-5@x" {
		t.Fatalf("overrides not set: %v", a.modelOverrides)
	}
	if !a.noThinking || !a.noStructured {
		t.Fatalf("toggles not set: thinking=%v structured=%v", a.noThinking, a.noStructured)
	}
}
