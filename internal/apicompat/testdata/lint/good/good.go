package good

// Old is retired.
//
// Deprecated: since v0.3. Use New instead.
//
//go:fix inline
func Old() int { return New() }

// Legacy has no replacement and a body with two statements.
//
// Deprecated: since v0.2. No replacement.
func Legacy() int {
	x := New()
	return x
}

// Type is retired.
//
// Deprecated: since v0.4. Use Other instead.
type Type int

// New is current.
func New() int { return 1 }
