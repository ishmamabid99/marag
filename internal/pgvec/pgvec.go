// Package pgvec encodes float32 vectors as pgvector text literals so callers
// can insert/query without depending on a pgvector Go driver.
package pgvec

import (
	"strconv"
	"strings"
)

// Literal renders a vector as the pgvector text form "[f1,f2,...]", suitable
// for a `$n::vector` cast. Uses minimal round-trippable float32 formatting.
func Literal(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 8)
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
