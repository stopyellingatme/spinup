package bad

// A has no version.
//
// Deprecated: Use New instead.
type A int

// B has a bad separator.
//
// Deprecated: since v0.2 use New.
var B int

// Forward forwards to New but has no inline directive.
//
// Deprecated: since v0.2. Use New instead.
func Forward() int { return New() }

// C has no final period.
//
// Deprecated: since v0.3. Use New instead
const C = 1

// New is current.
func New() int { return 1 }
