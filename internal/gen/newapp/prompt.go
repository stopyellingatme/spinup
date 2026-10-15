package newapp

import (
	"context"
	"fmt"
	"io"
	"strings"

	"charm.land/huh/v2"
	"github.com/stopyellingatme/spinup/internal/render"
	"github.com/stopyellingatme/spinup/internal/templates"
)

// Prompter obtains one answer, with a proposed default value.
type Prompter interface {
	// Ask returns an answer for the question and its proposed default.
	Ask(context.Context, templates.Question, any) (any, error)
}

// HuhPrompter displays pack questions on the supplied terminal streams.
type HuhPrompter struct {
	In  io.Reader
	Out io.Writer
}

// Ask displays an input, confirm, or select field for a pack question.
func (p HuhPrompter) Ask(ctx context.Context, q templates.Question, value any) (any, error) {
	var field huh.Field
	text, _ := value.(string)
	boolean, _ := value.(bool)
	switch q.Type {
	case "bool":
		field = huh.NewConfirm().Title(q.Prompt).Value(&boolean)
	case "enum":
		field = huh.NewSelect[string]().Title(q.Prompt).Options(huh.NewOptions(q.Choices...)...).Value(&text)
	default:
		field = huh.NewInput().Title(q.Prompt).Value(&text)
	}
	if err := huh.NewForm(huh.NewGroup(field)).WithInput(p.In).WithOutput(p.Out).RunWithContext(ctx); err != nil {
		return nil, fmt.Errorf("cannot read answer %q: %w\nTry: pass --%s", q.Key, err, strings.ReplaceAll(q.Key, "_", "-"))
	}
	if q.Type == "bool" {
		return boolean, nil
	}
	return text, nil
}

func completeAnswers(ctx context.Context, o Options, questions []templates.Question, prompter Prompter) (map[string]any, []string, error) {
	answers := make(map[string]any, len(o.Answers)+1)
	for key, value := range o.Answers {
		answers[key] = value
	}
	answers["app_name"] = o.Name
	var warnings []string
	for _, q := range questions {
		if _, ok := answers[q.Key]; ok {
			continue
		}
		var value any
		if q.HasDefault() {
			value = q.Default
			if q.Type == "bool" {
				value = q.Default == "true"
			}
		}
		if q.Key == "module" {
			value = "example.com/" + o.Name
		}
		if o.Interactive {
			if prompter == nil {
				return nil, nil, fmt.Errorf("cannot prompt for %q: no prompter is available\nTry: pass --%s", q.Key, strings.ReplaceAll(q.Key, "_", "-"))
			}
			var err error
			value, err = prompter.Ask(ctx, q, value)
			if err != nil {
				return nil, nil, err
			}
		}
		if value != nil {
			answers[q.Key] = value
		}
		if q.Key == "module" && value == "example.com/"+o.Name {
			warnings = append(warnings, "Using example.com/"+o.Name+" as the Go module. Pass --module to choose your module path.")
		}
	}
	// Validate before pack selection so modules have their canonical []string type.
	completed, err := render.ValidateAnswers([]*templates.Pack{{Questions: questions}}, answers)
	if err != nil && strings.Contains(err.Error(), `validate answer "app_name"`) {
		return nil, nil, fmt.Errorf("invalid app_name %q: the name does not match the pack pattern\nTry: pass an application name such as todo", o.Name)
	}
	return completed, warnings, err
}
