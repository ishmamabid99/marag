package pgvec

import "testing"

func TestLiteral(t *testing.T) {
	tests := []struct {
		name string
		in   []float32
		want string
	}{
		{"empty", []float32{}, "[]"},
		{"single", []float32{0.5}, "[0.5]"},
		{"multi", []float32{0.1, -0.2, 0.3}, "[0.1,-0.2,0.3]"},
		{"integers", []float32{1, 2, 3}, "[1,2,3]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Literal(tt.in); got != tt.want {
				t.Errorf("Literal(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
