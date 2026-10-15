package run

import (
	"context"
	"fmt"
	"io"
	"strings"

	"charm.land/huh/v2"
	"github.com/stopyellingatme/spinup/internal/gen"
)

// Prompter selects a generator and reads its arguments as one line.
type Prompter interface {
	// Prompt returns the selected kind and its argument line.
	Prompt(context.Context, []gen.Generator) (kind string, line string, err error)
}

// HuhPrompter displays generator questions on the supplied terminal streams.
type HuhPrompter struct {
	// In supplies terminal input.
	In io.Reader
	// Out receives the prompt display.
	Out io.Writer
}

// Prompt displays the registered kinds and then requests the selected kind's arguments.
func (p HuhPrompter) Prompt(ctx context.Context, kinds []gen.Generator) (string, string, error) {
	options := make([]huh.Option[string], 0, len(kinds))
	for _, g := range kinds {
		options = append(options, huh.NewOption(g.Name+" — "+g.Short, g.Name))
	}
	var kind, line string
	selectKind := huh.NewSelect[string]().Title("Generator kind").Options(options...).Value(&kind)
	if err := huh.NewForm(huh.NewGroup(selectKind)).WithInput(p.In).WithOutput(p.Out).RunWithContext(ctx); err != nil {
		return "", "", err
	}
	usage := "Arguments and flags"
	for _, g := range kinds {
		if g.Name == kind && g.Usage != "" {
			usage += " (" + g.Usage + ")"
		}
	}
	input := huh.NewInput().Title(usage).Value(&line)
	if err := huh.NewForm(huh.NewGroup(input)).WithInput(p.In).WithOutput(p.Out).RunWithContext(ctx); err != nil {
		return "", "", err
	}
	return kind, line, nil
}

// Pick requests and validates a generator selection and splits its argument line at spaces.
func Pick(ctx context.Context, kinds []gen.Generator, p Prompter) (gen.Generator, []string, error) {
	if len(kinds) == 0 {
		return gen.Generator{}, nil, fmt.Errorf("cannot select a generator: none are registered\nTry: use a spinup build with registered generators")
	}
	kind, line, err := p.Prompt(ctx, kinds)
	if err != nil {
		return gen.Generator{}, nil, withHint("read generator selection", err, "spinup g <kind> [args...]")
	}
	for _, g := range kinds {
		if g.Name == kind {
			return g, strings.Fields(line), nil
		}
		for _, alias := range g.Aliases {
			if alias == kind {
				return g, strings.Fields(line), nil
			}
		}
	}
	return gen.Generator{}, nil, fmt.Errorf("cannot select generator %q: the kind is not registered\nTry: spinup g <kind> --help", kind)
}
