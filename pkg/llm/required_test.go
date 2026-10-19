package llm

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRequiredFields(t *testing.T) {
	// Go-literal schema.
	if got := requiredFields([]string{"a", "b"}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("[]string = %v", got)
	}
	// JSON-decoded schema decodes arrays as []any — must still be honored.
	var decoded map[string]any
	if err := json.Unmarshal([]byte(`{"required":["path","content"]}`), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := requiredFields(decoded["required"]); !reflect.DeepEqual(got, []string{"path", "content"}) {
		t.Fatalf("[]any = %v, want [path content]", got)
	}
	// Absent/other → nil, no panic.
	if got := requiredFields(nil); got != nil {
		t.Fatalf("nil = %v", got)
	}
	if got := requiredFields([]any{"ok", 42, "also"}); !reflect.DeepEqual(got, []string{"ok", "also"}) {
		t.Fatalf("mixed = %v, want [ok also]", got)
	}
}
