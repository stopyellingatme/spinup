// Package mcp plans typed MCP tools and their registration file.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/pflag"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/project"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

// GeneratedPath is the managed registration file for generated tools.
const GeneratedPath = "api/internal/mcp/tools_gen.go"

func init() {
	registry.Register(gen.Generator{Name: "mcp", Short: "Generate a typed MCP tool", Usage: "tool <name>", Example: "  # Add a write tool\n  spinup g mcp tool lookup_order --write", Flags: flags, Run: plan})
}

func flags(fs *pflag.FlagSet) {
	fs.Bool("write", false, "allow the tool to write")
	fs.Bool("destructive", false, "mark the tool as destructive (implies --write)")
	fs.StringSlice("role", nil, "declared roles that can call the tool (repeatable)")
}

// RequirePack rejects generation when the lock has no MCP pack.
func RequirePack(lock *manifest.Lock) error {
	if lock == nil || !slices.Contains(lock.Packs, "mcp") {
		return fmt.Errorf("cannot generate MCP tools: the app has no mcp module\nTry: spinup module add mcp")
	}
	return nil
}

var snakeName = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)

func toolName(name string) (string, error) {
	if len(name) > 64 || !snakeName.MatchString(name) {
		return "", fmt.Errorf("cannot generate MCP tool %q: the name must be snake_case with at most 64 characters\nTry: spinup g mcp tool lookup_order", name)
	}
	var camel strings.Builder
	for _, word := range strings.Split(name, "_") {
		camel.WriteString(strings.ToUpper(word[:1]) + word[1:])
	}
	return camel.String(), nil
}

func boolFlag(fs *pflag.FlagSet, name string) bool {
	if fs == nil {
		return false
	}
	value, _ := fs.GetBool(name)
	return value
}

func plan(_ context.Context, in gen.Input) (*gen.Output, error) {
	if len(in.Args) != 2 || in.Args[0] != "tool" {
		return nil, fmt.Errorf("cannot generate MCP tool: expected tool and one name\nTry: spinup g mcp tool <name>")
	}
	if err := RequirePack(in.Lock); err != nil {
		return nil, err
	}
	name := in.Args[1]
	camel, err := toolName(name)
	if err != nil {
		return nil, err
	}
	path := "api/internal/mcp/" + name + ".go"
	if _, err := os.Stat(filepath.Join(in.Root, path)); err == nil && !boolFlag(in.Flags, "force") {
		return nil, fmt.Errorf("cannot generate MCP tool: %s already exists\nTry: pass --force to replace the file", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("cannot inspect %s: %w\nTry: check the file permissions", path, err)
	}
	var roles []string
	if in.Flags != nil && in.Flags.Changed("role") {
		roles, err = in.Flags.GetStringSlice("role")
		if err != nil {
			return nil, fmt.Errorf("cannot read --role: %w\nTry: pass --role manager", err)
		}
		cfg, err := project.Load(in.Root)
		if err != nil {
			return nil, fmt.Errorf("cannot read spinup.toml: %w\nTry: correct spinup.toml", err)
		}
		declared := cfg.AuthRoles()
		if err := declared.Validate(); err != nil {
			return nil, err
		}
		if len(roles) == 0 {
			return nil, fmt.Errorf("cannot generate a role-guarded MCP tool: the --role list is empty\nTry: pass --role manager")
		}
		for _, role := range roles {
			if !slices.Contains(declared.Names, role) {
				return nil, fmt.Errorf("cannot generate a role-guarded MCP tool: role %q is not declared\nTry: use one of: %s, or add the role to [auth] and run spinup g roles", role, strings.Join(declared.Names, ", "))
			}
		}
	}
	source := fmt.Sprintf(`package mcp

import (
 "context"

 kitmcp "github.com/stopyellingatme/spinup/kit/x/mcp"
)

// %sIn holds the tool arguments.
type %sIn struct {}

// %sOut holds the tool result.
type %sOut struct {}

// spinup:mcp-tool register%s
func register%s(s *kitmcp.Server, d Deps) {
 kitmcp.Tool(s, kitmcp.ToolDef{
 Name: %q,
 Description: %q,
 ReadOnly: %t,
 Destructive: %t,
 Roles: %#v,
 }, func(ctx context.Context, in %sIn) (%sOut, error) {
 return %sOut{}, kitmcp.ToolError("not implemented")
 })
}
`, camel, camel, camel, camel, camel, camel, name, "Runs "+strings.ReplaceAll(name, "_", " ")+".", !boolFlag(in.Flags, "write") && !boolFlag(in.Flags, "destructive"), boolFlag(in.Flags, "destructive"), roles, camel, camel, camel)
	data, err := format.Source([]byte(source))
	if err != nil {
		return nil, fmt.Errorf("cannot format MCP tool: %w\nTry: report this generator bug", err)
	}
	file := vfs.File{Path: path, Data: data, Class: string(manifest.Scaffold)}
	generated, err := RenderGenerated(in.Root, []vfs.File{file})
	if err != nil {
		return nil, err
	}
	return &gen.Output{Plan: vfs.Plan{Files: []vfs.File{file, generated}}, Edits: []string{GeneratedPath}}, nil
}
