package render

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"text/template"
	"unicode"

	"github.com/stopyellingatme/spinup/internal/stackversions"
)

func functions(stack *stackversions.Stack) template.FuncMap {
	return template.FuncMap{
		"lower":   strings.ToLower,
		"upper":   strings.ToUpper,
		"snake":   func(s string) string { return strings.Join(words(s), "_") },
		"kebab":   func(s string) string { return strings.Join(words(s), "-") },
		"camel":   func(s string) string { return titleWords(s, false) },
		"pascal":  func(s string) string { return titleWords(s, true) },
		"quote":   strconv.Quote,
		"join":    func(separator string, values []string) string { return strings.Join(values, separator) },
		"indent":  indent,
		"default": defaultValue,
		"has":     func(value string, values []string) bool { return slices.Contains(values, value) },
		"image": func(key string) (string, error) {
			if stack == nil {
				return "", fmt.Errorf("image %q: stack versions are missing", key)
			}
			image, err := stack.Image(key)
			if err != nil {
				return "", err
			}
			return image.Ref(), nil
		},
		"npm": func(pkg string) (string, error) {
			if stack == nil {
				return "", fmt.Errorf("npm %q: stack versions are missing", pkg)
			}
			return stack.NPMVersion(pkg)
		},
		"gomod": func(path string) (string, error) {
			if stack == nil {
				return "", fmt.Errorf("gomod %q: stack versions are missing", path)
			}
			return stack.GoModule(path)
		},
		"uses": func(name string) (string, error) {
			if stack == nil {
				return "", fmt.Errorf("uses %q: stack versions are missing", name)
			}
			action, err := stack.Action(name)
			if err != nil {
				return "", err
			}
			return action.Uses(name), nil
		},
	}
}

// words splits identifiers at punctuation and camel-case or acronym boundaries.
func words(s string) []string {
	runes := []rune(s)
	var parts []string
	start := 0
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			if start < i {
				parts = append(parts, strings.ToLower(string(runes[start:i])))
			}
			start = i + 1
			continue
		}
		if i <= start {
			continue
		}
		prev := runes[i-1]
		boundary := unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev) || unicode.IsUpper(prev) && i+1 < len(runes) && unicode.IsLower(runes[i+1]))
		if boundary {
			parts = append(parts, strings.ToLower(string(runes[start:i])))
			start = i
		}
	}
	if start < len(runes) {
		parts = append(parts, strings.ToLower(string(runes[start:])))
	}
	return parts
}

func titleWords(s string, pascal bool) string {
	parts := words(s)
	for i, part := range parts {
		if i == 0 && !pascal {
			continue
		}
		chars := []rune(part)
		chars[0] = unicode.ToUpper(chars[0])
		parts[i] = string(chars)
	}
	return strings.Join(parts, "")
}

func indent(width int, s string) (string, error) {
	if width < 0 {
		return "", fmt.Errorf("indent width must not be negative")
	}
	if s == "" {
		return "", nil
	}
	prefix := strings.Repeat(" ", width)
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix), nil
}

func defaultValue(fallback, value any) any {
	if value == nil {
		return fallback
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		if v.Len() == 0 {
			return fallback
		}
	case reflect.Bool:
		if !v.Bool() {
			return fallback
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if v.Int() == 0 {
			return fallback
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if v.Uint() == 0 {
			return fallback
		}
	case reflect.Float32, reflect.Float64:
		if v.Float() == 0 {
			return fallback
		}
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return fallback
		}
	}
	return value
}
