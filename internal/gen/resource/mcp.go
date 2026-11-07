package resource

import (
	"fmt"
	"go/format"
	"strings"

	"github.com/stopyellingatme/spinup/internal/gen"
	mcpgenerator "github.com/stopyellingatme/spinup/internal/gen/mcp"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

func addMCP(spec Spec, in gen.Input, out *gen.Output) (*gen.Output, error) {
	if err := mcpgenerator.RequirePack(in.Lock); err != nil {
		return nil, err
	}
	module, err := apiModule(spec.Root)
	if err != nil {
		return nil, err
	}
	data, err := mcpTools(spec, module)
	if err != nil {
		return nil, err
	}
	file := vfs.File{Path: "api/internal/mcp/" + spec.Name.PluralSnake() + "_tools.go", Data: data, Class: string(manifest.Scaffold)}
	generated, err := mcpgenerator.RenderGenerated(spec.Root, []vfs.File{file})
	if err != nil {
		return nil, err
	}
	out.Plan, err = out.Plan.Merge(vfs.Plan{Files: []vfs.File{file, generated}})
	if err != nil {
		return nil, fmt.Errorf("cannot plan MCP tools: %w\nTry: report this generator bug", err)
	}
	out.Edits = append(out.Edits, mcpgenerator.GeneratedPath)
	return out, nil
}

func mcpTools(spec Spec, module string) ([]byte, error) {
	var src strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&src, format+"\n", args...) }
	singular, plural := spec.Name.Pascal(), spec.Name.PluralPascal()
	prefix := spec.Name.PluralCamel()
	p("package mcp\n\nimport (\n\"context\"\n\"errors\"\n\"fmt\"\n\n\"github.com/jackc/pgx/v5\"\n\"github.com/stopyellingatme/spinup/kit/httpx\"\nkitmcp \"github.com/stopyellingatme/spinup/kit/x/mcp\"\n%q\n%q\n)", module+"/internal/gen", module+"/internal/handlers")
	p("type %sListIn struct {", prefix)
	p("Limit int32 `json:\"limit,omitempty\" jsonschema:\"Page size, default 50, maximum 200\"`")
	p("Offset int32 `json:\"offset,omitempty\" jsonschema:\"Number of records to skip\"`\n}")
	p("type %sListOut struct {\nItems []gen.%s `json:\"items\"`\nTotal int64 `json:\"total\"`\n}", prefix, singular)
	p("type %sIDIn struct {\nID int64 `json:\"id\"`\n}", prefix)
	p("type %sUpdateIn struct {\nID int64 `json:\"id\"`\ngen.%sInput\n}", prefix, singular)
	p("type %sDeleteOut struct {\nDeleted bool `json:\"deleted\"`\n}", prefix)
	p("// spinup:mcp-tool register%sTools\nfunc register%sTools(s *kitmcp.Server, d Deps) {", plural, plural)
	p("server := handlers.NewServer(d.DB)")
	for _, op := range []string{"List", "Get", "Create", "Update", "Delete"} {
		suffix := singular
		snake := spec.Name.Snake()
		if op == "List" {
			suffix = plural
			snake = spec.Name.PluralSnake()
		}
		method := op + suffix
		input, output := prefix+"IDIn", "gen."+singular
		switch op {
		case "List":
			input = prefix + "ListIn"
			output = prefix + "ListOut"
		case "Create":
			input = "gen." + singular + "Input"
		case "Update":
			input = prefix + "UpdateIn"
		case "Delete":
			output = prefix + "DeleteOut"
		}
		p("kitmcp.Tool(s, kitmcp.ToolDef{")
		p("Name: %q, Description: %q,", strings.ToLower(op)+"_"+snake, op+" "+strings.ReplaceAll(snake, "_", " ")+".")
		p("ReadOnly: %t, Destructive: %t, Idempotent: %t, Roles: %#v,", op == "List" || op == "Get", op == "Delete", op == "Update" || op == "Delete", spec.Roles)
		p("}, func(ctx context.Context, in %s) (%s, error) {", input, output)
		zero := output + "{}"
		if op == "List" {
			p("limit := in.Limit\nif limit == 0 { limit = 50 }")
			p("if in.Limit < 0 || in.Limit > 200 || in.Offset < 0 || in.Offset > 10000 { return %s, kitmcp.ToolError(\"The page is invalid. Try: use limit from 1 to 200 and offset from 0 to 10000.\") }", zero)
		}
		p("response, err := server.%s(ctx, gen.%sRequestObject{", method, method)
		switch op {
		case "List":
			p("Params: gen.%sParams{Limit: &limit, Offset: &in.Offset},", method)
		case "Get", "Delete":
			p("Id: in.ID,")
		case "Create":
			p("Body: &in,")
		case "Update":
			p("Id: in.ID, Body: &in.%sInput,", singular)
		}
		p("})\nif err != nil { return %s, %sToolError(err) }", zero, prefix)
		p("switch result := response.(type) {")
		status := "200JSONResponse"
		if op == "Create" {
			status = "201JSONResponse"
		}
		if op == "Delete" {
			status = "204Response"
		}
		p("case gen.%s%s:", method, status)
		if op == "Delete" {
			p("return %s{Deleted: true}, nil", output)
		} else {
			p("return %s(result), nil", output)
		}
		if op == "Get" || op == "Update" || op == "Delete" {
			p("case gen.%s404ApplicationProblemPlusJSONResponse:\nreturn %s, %sToolDetail(result.Detail)", method, zero, prefix)
		}
		if op == "List" || op == "Create" || op == "Update" {
			p("case gen.%s422ApplicationProblemPlusJSONResponse:\nreturn %s, %sToolDetail(result.Detail)", method, zero, prefix)
		}
		p("default:\nreturn %s, fmt.Errorf(\"unexpected %s response %%T\", response)\n}\n})", zero, method)
	}
	p("}")
	p(`
func %sToolDetail(detail *string) error {
 if detail == nil { return kitmcp.ToolError("The request failed. Try: check the arguments.") }
 return kitmcp.ToolError("%%s Try: check the arguments.", *detail)
}

func %sToolError(err error) error {
 var problem *httpx.Error
 if errors.As(err, &problem) && (problem.Problem.Status == 404 || problem.Problem.Status == 422) {
  return %sToolDetail(&problem.Problem.Detail)
 }
 if errors.Is(err, pgx.ErrNoRows) {
  return kitmcp.ToolError("The record does not exist. Try: use an existing id.")
 }
 return err
}
`, prefix, prefix, prefix)
	data, err := format.Source([]byte(src.String()))
	if err != nil {
		return nil, fmt.Errorf("cannot format MCP resource tools: %w\nTry: report this generator bug", err)
	}
	return data, nil
}
