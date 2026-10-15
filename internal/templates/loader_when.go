package templates

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

var keyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type predicate struct{ key, op, value string }

// condition is a disjunction of conjunctions. Evaluation checks every predicate.
type condition struct {
	source string
	groups [][]predicate
}

func parseCondition(source string) (*condition, error) {
	c := &condition{source: source}
	rest := strings.TrimSpace(source)
	if rest == "" {
		return c, nil
	}
	var group []predicate
	for {
		atom := predicate{op: "bool"}
		if strings.HasPrefix(rest, "!") {
			atom.op = "not"
			rest = strings.TrimSpace(rest[1:])
		}
		n := 0
		for n < len(rest) && (rest[n] == '_' || rest[n] >= 'a' && rest[n] <= 'z' || rest[n] >= 'A' && rest[n] <= 'Z' || n > 0 && rest[n] >= '0' && rest[n] <= '9') {
			n++
		}
		if n == 0 {
			return nil, invalidCondition(source)
		}
		atom.key, rest = rest[:n], strings.TrimSpace(rest[n:])
		for _, op := range []string{"==", "!=", "contains"} {
			if !strings.HasPrefix(rest, op) {
				continue
			}
			if atom.op == "not" {
				return nil, invalidCondition(source)
			}
			if op == "contains" && len(rest) > len(op) && !unicode.IsSpace(rune(rest[len(op)])) {
				return nil, invalidCondition(source)
			}
			atom.op = op
			rest = strings.TrimSpace(rest[len(op):])
			if !strings.HasPrefix(rest, "'") {
				return nil, invalidCondition(source)
			}
			end := strings.IndexByte(rest[1:], '\'')
			if end < 0 {
				return nil, invalidCondition(source)
			}
			atom.value = rest[1 : end+1]
			rest = strings.TrimSpace(rest[end+2:])
			break
		}
		group = append(group, atom)
		if rest == "" {
			c.groups = append(c.groups, group)
			return c, nil
		}
		switch {
		case strings.HasPrefix(rest, "&&"):
			rest = strings.TrimSpace(rest[2:])
		case strings.HasPrefix(rest, "||"):
			c.groups = append(c.groups, group)
			group = nil
			rest = strings.TrimSpace(rest[2:])
		default:
			return nil, invalidCondition(source)
		}
	}
}

func invalidCondition(source string) error {
	return fmt.Errorf("invalid when expression %q\nTry: use keys, single-quoted values, &&, and ||", source)
}

// Matches evaluates When against answers. Unknown keys and wrong types are errors.
// Load compiles conditions once. Directly constructed FileSpecs compile on demand.
// All predicates are checked, including predicates in branches with known results.
func (f FileSpec) Matches(answers map[string]any) (bool, error) {
	c := f.condition
	if c == nil || c.source != f.When {
		var err error
		c, err = parseCondition(f.When)
		if err != nil {
			return false, err
		}
	}
	if len(c.groups) == 0 {
		return true, nil
	}
	result := false
	for _, group := range c.groups {
		conjunction := true
		for _, atom := range group {
			match, err := atom.matches(answers)
			if err != nil {
				return false, fmt.Errorf("evaluate when %q: %w\nTry: supply answers with the declared types", f.When, err)
			}
			conjunction = conjunction && match
		}
		result = result || conjunction
	}
	return result, nil
}

func (p predicate) matches(answers map[string]any) (bool, error) {
	v, ok := answers[p.key]
	if !ok {
		return false, fmt.Errorf("unknown answer key %q", p.key)
	}
	switch p.op {
	case "bool", "not":
		b, ok := v.(bool)
		if !ok {
			return false, fmt.Errorf("answer %q must be a bool", p.key)
		}
		if p.op == "not" {
			b = !b
		}
		return b, nil
	case "==", "!=":
		s, ok := v.(string)
		if !ok {
			return false, fmt.Errorf("answer %q must be a string", p.key)
		}
		if p.op == "!=" {
			return s != p.value, nil
		}
		return s == p.value, nil
	case "contains":
		// Answers decoded from spinup.lock hold lists as []any; validated answers hold []string.
		switch values := v.(type) {
		case []string:
			return slices.Contains(values, p.value), nil
		case []any:
			return slices.Contains(values, any(p.value)), nil
		}
		return false, fmt.Errorf("answer %q must be a string slice", p.key)
	default:
		return false, invalidCondition(p.op)
	}
}
