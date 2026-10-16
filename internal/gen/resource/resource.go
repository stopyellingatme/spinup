package resource

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/pflag"

	"github.com/stopyellingatme/spinup/internal/db/ops"
	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/fields"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/ui"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

func init() {
	registry.Register(gen.Generator{
		Name:  "resource",
		Short: "Generate a CRUD API resource: migration, queries, OpenAPI, handlers, and tests",
		Usage: "<name> <field:type>...",
		Flags: func(fs *pflag.FlagSet) {
			fs.Bool("api-only", false, "skip the parts that other packs register, such as web pages")
			fs.Bool("no-tests", false, "skip the handler test file")
			fs.Bool("shared", false, "share rows between signed-in users (requires auth)")
		},
		Run: run,
	})
}

// Spec is the resource that the generator and its parts plan.
type Spec struct {
	// Root is the project root.
	Root string
	// Name is the resource name.
	Name fields.Name
	// Fields are the writable fields, in argument order.
	Fields []fields.Field
	// Owned reports whether each row belongs to its signed-in user.
	Owned bool
	// Shared reports whether auth protects a table shared by all signed-in users.
	Shared bool
}

// Part plans extra files for each resource, such as web pages.
type Part struct {
	// Name identifies the part. Parts run in name order.
	Name string
	// Plan returns the files of the part and the post-steps they need.
	Plan func(ctx context.Context, spec Spec) (vfs.Plan, []gen.PostStep, error)
	// Paths, when set, returns the files that the part owns for a resource,
	// relative to the project root. Paths includes them.
	Paths func(name fields.Name) []string
}

var (
	partsMu sync.RWMutex
	parts   []Part
)

// RegisterPart adds a part to every later resource. It panics for an empty
// name, a nil Plan, or a duplicate name.
func RegisterPart(p Part) {
	partsMu.Lock()
	defer partsMu.Unlock()
	if p.Name == "" || p.Plan == nil {
		panic("resource part needs a name and a Plan")
	}
	for _, existing := range parts {
		if existing.Name == p.Name {
			panic(fmt.Sprintf("duplicate resource part %q", p.Name))
		}
	}
	parts = append(parts, p)
}

func registeredParts() []Part {
	partsMu.RLock()
	defer partsMu.RUnlock()
	sorted := append([]Part(nil), parts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	return sorted
}

// The paths that the built-in parts write, relative to the project root.
const (
	serverPath         = "api/internal/handlers/server.go"
	requiredFieldsPath = "api/internal/handlers/validation.go"
	routesGenPath      = "api/internal/routes/routes_gen.go"
)

// QueriesPath returns the sqlc queries file of a resource.
func QueriesPath(name fields.Name) string { return "api/db/queries/" + name.PluralSnake() + ".sql" }

// SchemasPath returns the OpenAPI schemas file of a resource.
func SchemasPath(name fields.Name) string {
	return "api/openapi/schemas/" + name.Snake() + ".yaml"
}

func openapiPathsPath(name fields.Name) string {
	return "api/openapi/paths/" + name.PluralSnake() + ".yaml"
}

func handlersPath(name fields.Name) string {
	return "api/internal/handlers/" + name.PluralSnake() + ".go"
}

func handlersTestPath(name fields.Name) string {
	return "api/internal/handlers/" + name.PluralSnake() + "_test.go"
}

// Paths returns the files that a resource owns, without its migration and
// without the files that all resources share (server.go and routes_gen.go).
// The list includes the files of registered parts. It is sorted.
func Paths(name fields.Name) []string {
	paths := []string{QueriesPath(name), openapiPathsPath(name), SchemasPath(name), handlersPath(name), handlersTestPath(name)}
	for _, p := range registeredParts() {
		if p.Paths != nil {
			paths = append(paths, p.Paths(name)...)
		}
	}
	sort.Strings(paths)
	return paths
}

// ParseSpec reads the resource name and fields from generator arguments.
// It refuses a resource without fields, a reference to a table that no
// migration creates, and a reference to the resource itself that is not
// nullable.
func ParseSpec(root string, args []string, usage string) (Spec, error) {
	if len(args) < 2 {
		return Spec{}, fmt.Errorf("expected a name and at least one field\nTry: spinup g %s", usage)
	}
	name, err := fields.ParseName(args[0])
	if err != nil {
		return Spec{}, err
	}
	if reservedResourceName(name) {
		return Spec{}, fmt.Errorf("resource name %q is reserved for the API reference\nTry: choose another name, for example document", args[0])
	}
	fs, err := fields.Parse(args[1:])
	if err != nil {
		return Spec{}, err
	}
	for _, f := range fs {
		if f.Type.Ref == "" {
			continue
		}
		if f.Type.Ref == name.Snake() {
			if !f.Nullable {
				return Spec{}, fmt.Errorf("field %q refers to %s itself, so it must be nullable: the first row has nothing to refer to\nTry: use %s:ref:%s?", f.Name.Snake(), name.Table(), f.Name.Snake(), f.Type.Ref)
			}
			continue
		}
		parent, _ := fields.ParseName(f.Type.Ref)
		_, ok, err := findTable(root, parent.Table())
		if err != nil {
			return Spec{}, err
		}
		if !ok {
			return Spec{}, fmt.Errorf("field %q refers to the table %s, but no migration in %s creates it\nTry: spinup g model %s <field:type>... first", f.Name.Snake(), parent.Table(), ops.MigrationsDir, parent.Snake())
		}
	}
	return Spec{Root: root, Name: name, Fields: fs}, nil
}

// PlanModel plans the migration, the queries file, and the schemas file of
// spec. When a migration already creates the table, PlanModel fails unless
// force is set. With force, it rewrites that migration when its name is
// NNNNN_create_<table>.sql, and plans no migration otherwise.
func PlanModel(spec Spec, force bool) (vfs.Plan, error) {
	table := spec.Name.Table()
	var plan vfs.Plan
	existing, ok, err := findTable(spec.Root, table)
	if err != nil {
		return vfs.Plan{}, err
	}
	migration := ""
	switch {
	case ok && !force:
		return vfs.Plan{}, fmt.Errorf("the table %s already exists in %s/%s\nTry: pass --force to generate the files again, or choose another name", table, ops.MigrationsDir, existing.File)
	case ok && strings.HasSuffix(existing.File, "_create_"+table+".sql"):
		migration = existing.File
	case !ok:
		migration, err = ops.NextMigrationName(migrationsDir(spec.Root), "create_"+table)
		if err != nil {
			return vfs.Plan{}, withTry(err)
		}
	}
	files := []vfs.File{
		{Path: QueriesPath(spec.Name), Data: queriesSQL(spec), Class: string(manifest.Scaffold)},
		{Path: SchemasPath(spec.Name), Data: schemasYAML(spec), Class: string(manifest.Scaffold)},
	}
	if migration != "" {
		files = append(files, vfs.File{Path: ops.MigrationsDir + "/" + migration, Data: migrationSQL(spec), Class: string(manifest.Seed)})
	}
	for _, f := range files {
		if err := plan.Add(f); err != nil {
			return vfs.Plan{}, err
		}
	}
	return plan, nil
}

// withTry keeps the Try line of a ui.Error in the error text.
func withTry(err error) error {
	var ue *ui.Error
	if errors.As(err, &ue) && ue.Try != "" {
		return fmt.Errorf("%w\nTry: %s", err, ue.Try)
	}
	return err
}

// Bool reads a boolean flag. A missing flag set or flag reads as false.
func Bool(flags *pflag.FlagSet, name string) bool {
	if flags == nil {
		return false
	}
	v, err := flags.GetBool(name)
	return err == nil && v
}

func run(ctx context.Context, in gen.Input) (*gen.Output, error) {
	return build(ctx, in, registeredParts())
}

// build plans a resource with the built-in parts and then parts, which must
// be in name order.
func build(ctx context.Context, in gen.Input, parts []Part) (*gen.Output, error) {
	spec, err := ParseSpec(in.Root, in.Args, "resource <name> <field:type>... (for example: spinup g resource todo title:string done:bool)")
	if err != nil {
		return nil, err
	}
	authenticated, err := hasAuth(in.Root)
	if err != nil {
		return nil, err
	}
	spec.Shared = Bool(in.Flags, "shared")
	if spec.Shared && !authenticated {
		return nil, fmt.Errorf("cannot generate a shared resource without the auth module\nTry: drop --shared")
	}
	spec.Owned = authenticated && !spec.Shared
	if spec.Owned {
		for _, f := range spec.Fields {
			if f.Column() == "user_id" {
				return nil, fmt.Errorf("field %q conflicts with the resource owner column\nTry: choose another field name", f.Column())
			}
		}
	}
	plan, err := PlanModel(spec, Bool(in.Flags, "force"))
	if err != nil {
		return nil, err
	}
	apiPlan, err := planAPI(spec, !Bool(in.Flags, "no-tests"))
	if err != nil {
		return nil, err
	}
	if plan, err = plan.Merge(apiPlan); err != nil {
		return nil, fmt.Errorf("%w\nTry: report this generator bug", err)
	}
	steps := []gen.PostStep{gen.PostOpenAPI, gen.PostSQLC, gen.PostGofmt}
	if !Bool(in.Flags, "api-only") {
		for _, p := range parts {
			partPlan, partSteps, err := p.Plan(ctx, spec)
			if err != nil {
				return nil, fmt.Errorf("resource part %q: %w", p.Name, err)
			}
			if plan, err = plan.Merge(partPlan); err != nil {
				return nil, fmt.Errorf("resource part %q: %w", p.Name, err)
			}
			steps = append(steps, partSteps...)
		}
	}
	return &gen.Output{
		Plan:      plan,
		PostSteps: steps,
		Next:      []string{"spinup db migrate up", "cd api && go test ./..."},
	}, nil
}

// planAPI plans the OpenAPI paths, the handlers, their test, and the shared
// server and route files.
func planAPI(spec Spec, tests bool) (vfs.Plan, error) {
	module, err := apiModule(spec.Root)
	if err != nil {
		return vfs.Plan{}, err
	}
	rc := newResourceCode(spec, module)
	handlers, err := rc.handlersGo()
	if err != nil {
		return vfs.Plan{}, err
	}
	routes, err := routesGo(module, spec.Owned || spec.Shared)
	if err != nil {
		return vfs.Plan{}, err
	}
	files := []vfs.File{
		{Path: openapiPathsPath(spec.Name), Data: pathsYAML(spec), Class: string(manifest.Scaffold)},
		{Path: handlersPath(spec.Name), Data: handlers, Class: string(manifest.Scaffold)},
		{Path: routesGenPath, Data: routes, Class: string(manifest.Managed)},
	}
	if tests {
		parents, err := parentInserts(spec.Root, spec)
		if err != nil {
			return vfs.Plan{}, err
		}
		test, err := rc.handlersTestGo(parents)
		if err != nil {
			return vfs.Plan{}, err
		}
		files = append(files, vfs.File{Path: handlersTestPath(spec.Name), Data: test, Class: string(manifest.Scaffold)})
	}
	for _, shared := range []struct{ path, source string }{
		{serverPath, serverGo},
		{requiredFieldsPath, requiredFieldsGo},
	} {
		if _, err := os.Stat(filepath.Join(spec.Root, filepath.FromSlash(shared.path))); errors.Is(err, os.ErrNotExist) {
			source, err := withModule(shared.source, module)
			if err != nil {
				return vfs.Plan{}, err
			}
			files = append(files, vfs.File{Path: shared.path, Data: source, Class: string(manifest.Scaffold)})
		} else if err != nil {
			return vfs.Plan{}, fmt.Errorf("check %s: %w\nTry: check the file permissions", shared.path, err)
		}
	}
	var plan vfs.Plan
	for _, f := range files {
		if err := plan.Add(f); err != nil {
			return vfs.Plan{}, err
		}
	}
	return plan, nil
}

// hasAuth reports whether the lock lists the auth module.
func hasAuth(root string) (bool, error) {
	lock, err := manifest.Load(filepath.Join(root, "spinup.lock"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read spinup.lock: %w\nTry: restore a valid spinup.lock", err)
	}
	return slices.Contains(lock.Packs, "auth"), nil
}
