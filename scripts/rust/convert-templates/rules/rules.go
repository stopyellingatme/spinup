// Package rules holds the per-pack conversion rules of the template converter
// (docs/design/rust-plan.md decision 3, ADR 0020 §3).
//
// This file is converter core (main loop only). The golden unit of a pack owns
// rules/<pack>.go and registers its rule there from init. The go tool ignores file names
// that start with "_", so the rules of the _sample pack live in sample.go.
package rules

import "fmt"

// Rule adjusts the converted output of one pack.
type Rule struct {
	// Fix rewrites the converted bytes of one file. path is the slash-separated path below
	// the pack directory. Fix returns data unchanged for a file that it does not fix. A nil
	// Fix changes nothing.
	Fix func(path string, data []byte) ([]byte, error)
}

var registered = map[string]Rule{}

// Register sets the rule of a pack. Call it from init in rules/<pack>.go.
func Register(pack string, r Rule) {
	if _, ok := registered[pack]; ok {
		panic(fmt.Sprintf("rules: pack %q has two rules", pack))
	}
	registered[pack] = r
}

// For returns the rule of a pack, and false when the pack has no rule.
func For(pack string) (Rule, bool) {
	r, ok := registered[pack]
	return r, ok
}
