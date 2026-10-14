package render

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/stopyellingatme/spinup/internal/templates"
)

// ValidateAnswers completes defaults and checks types, enum choices, and patterns.
// The modules string question accepts comma-separated strings, []string, or JSON
// string arrays. It returns []string. Input maps and slices remain unchanged.
// Identical questions can be shared by packs. Conflicting definitions are errors.
func ValidateAnswers(packs []*templates.Pack, answers map[string]any) (map[string]any, error) {
	completed := make(map[string]any, len(answers))
	keys := make([]string, 0, len(answers))
	for key := range answers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if reserved(key) {
			return nil, fmt.Errorf("validate answer %q: reserved template key\nTry: use a different answer key", key)
		}
		value := answers[key]
		if values, ok := value.([]string); ok {
			value = slices.Clone(values)
		}
		completed[key] = value
	}
	seen := map[string]templates.Question{}
	for _, p := range packs {
		if p == nil {
			return nil, fmt.Errorf("validate answers: pack is nil\nTry: load each template pack")
		}
		for _, q := range p.Questions {
			if err := q.Validate(); err != nil {
				return nil, err
			}
			if previous, ok := seen[q.Key]; ok && !sameQuestion(previous, q) {
				return nil, answerError(q, "conflicting question definitions across packs")
			}
			seen[q.Key] = q
			value, ok := completed[q.Key]
			if !ok {
				if !q.HasDefault() {
					return nil, answerError(q, "required answer is missing")
				}
				value = q.Default
				if q.Type == "bool" {
					value = q.Default == "true"
				}
			}
			value, err := validateValue(q, value)
			if err != nil {
				return nil, err
			}
			completed[q.Key] = value
		}
	}
	return completed, nil
}

func sameQuestion(a, b templates.Question) bool {
	return a.Key == b.Key && a.Prompt == b.Prompt && a.Type == b.Type && a.Default == b.Default && a.HasDefault() == b.HasDefault() && a.Pattern == b.Pattern && slices.Equal(a.Choices, b.Choices)
}

func validateValue(q templates.Question, value any) (any, error) {
	if q.Key == "modules" && q.Type == "string" {
		values, err := moduleValues(value)
		if err != nil {
			return nil, answerError(q, err.Error())
		}
		for _, entry := range values {
			if err := checkPattern(q, entry); err != nil {
				return nil, err
			}
		}
		return values, nil
	}
	switch q.Type {
	case "bool":
		if _, ok := value.(bool); !ok {
			return nil, answerError(q, "expected a bool")
		}
	case "string", "enum":
		text, ok := value.(string)
		if !ok {
			return nil, answerError(q, "expected a string")
		}
		if q.Type == "enum" && !slices.Contains(q.Choices, text) {
			return nil, answerError(q, "value is not one of the enum choices")
		}
		if err := checkPattern(q, text); err != nil {
			return nil, err
		}
	}
	return value, nil
}

func moduleValues(value any) ([]string, error) {
	switch values := value.(type) {
	case string:
		result := []string{}
		for _, item := range strings.Split(values, ",") {
			item = strings.TrimSpace(item)
			if item != "" {
				result = append(result, item)
			}
		}
		return result, nil
	case []string:
		return append([]string{}, values...), nil
	case []any:
		result := make([]string, 0, len(values))
		for _, item := range values {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("expected a string slice")
			}
			result = append(result, text)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("expected a string slice or comma-separated string")
	}
}

func checkPattern(q templates.Question, value string) error {
	if q.Pattern == "" {
		return nil
	}
	pattern, err := regexp.Compile(q.Pattern)
	if err != nil {
		return answerError(q, fmt.Sprintf("invalid pattern: %v", err))
	}
	if !pattern.MatchString(value) {
		return answerError(q, "value does not match the pattern")
	}
	return nil
}

func answerError(q templates.Question, reason string) error {
	return fmt.Errorf("validate answer %q: %s\nTry: supply a valid --%s value", q.Key, reason, strings.ReplaceAll(q.Key, "_", "-"))
}
func reserved(key string) bool { return key == "Stack" || key == "TemplateVersion" }
