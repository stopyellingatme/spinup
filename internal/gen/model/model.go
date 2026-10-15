package model

import (
	"context"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/gen/resource"
)

func init() {
	registry.Register(gen.Generator{
		Name:  "model",
		Short: "Generate a table: migration, sqlc queries, and OpenAPI schemas",
		Usage: "<name> <field:type>...",
		Run:   run,
	})
}

func run(_ context.Context, in gen.Input) (*gen.Output, error) {
	spec, err := resource.ParseSpec(in.Root, in.Args, "model <name> <field:type>... (for example: spinup g model tag name:string)")
	if err != nil {
		return nil, err
	}
	plan, err := resource.PlanModel(spec, resource.Bool(in.Flags, "force"))
	if err != nil {
		return nil, err
	}
	return &gen.Output{
		Plan:      plan,
		PostSteps: []gen.PostStep{gen.PostOpenAPI, gen.PostSQLC},
		Next:      []string{"spinup db migrate up", "cd api && go build ./..."},
	}, nil
}
