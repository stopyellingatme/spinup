package resource

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

// kitJSONKeys returns the JSON keys of a struct in kit/audit/audit.go. The
// generated history handler converts audit.Event to the schema with a JSON
// round trip, so a key that differs from the schema loses its value.
func kitJSONKeys(t *testing.T, name string) []string {
	t.Helper()
	path := filepath.Join(testutil.RepoRoot(t), "kit/audit/audit.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.TypeSpec)
		if !ok || spec.Name.Name != name {
			return true
		}
		for _, field := range spec.Type.(*ast.StructType).Fields.List {
			if field.Tag == nil {
				continue
			}
			tag, err := strconv.Unquote(field.Tag.Value)
			if err != nil {
				t.Fatal(err)
			}
			key, _, _ := strings.Cut(reflect.StructTag(tag).Get("json"), ",")
			if key != "" && key != "-" {
				keys = append(keys, key)
			}
		}
		return false
	})
	if len(keys) == 0 {
		t.Fatalf("no JSON keys for %s in %s", name, path)
	}
	slices.Sort(keys)
	return keys
}

type schemaObject struct {
	Required   []string                `yaml:"required"`
	Properties map[string]schemaObject `yaml:"properties"`
}

func propertyNames(o schemaObject) []string {
	var names []string
	for name := range o.Properties {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func TestAuditSchemaMatchesKitEvent(t *testing.T) {
	t.Parallel()
	var schemas map[string]schemaObject
	if err := yaml.Unmarshal([]byte(auditSchemas), &schemas); err != nil {
		t.Fatal(err)
	}
	event := schemas["AuditEvent"]
	if got, want := propertyNames(event), kitJSONKeys(t, "Event"); !slices.Equal(got, want) {
		t.Errorf("AuditEvent properties = %v, want the audit.Event JSON keys %v", got, want)
	}
	if got, want := propertyNames(event.Properties["actor"]), kitJSONKeys(t, "Actor"); !slices.Equal(got, want) {
		t.Errorf("AuditEvent actor properties = %v, want the audit.Actor JSON keys %v", got, want)
	}
	for _, name := range event.Required {
		if _, ok := event.Properties[name]; !ok {
			t.Errorf("AuditEvent requires %s, which is not a property", name)
		}
	}
	if !slices.Contains(event.Required, "table_name") {
		t.Errorf("AuditEvent required = %v, want table_name", event.Required)
	}
}

// TestHistoryTestChecksSubject proves that the generated handler test decodes the
// history response and checks table_name and record_id of each event.
func TestHistoryTestChecksSubject(t *testing.T) {
	t.Parallel()
	root := renderApp(t, []string{"base", "api", "web", "auth", "audit"}, []string{"auth", "audit"})
	out, err := run(t.Context(), gen.Input{Root: root, Args: []string{"note", "title:string"}, Flags: flagSet(t, "--api-only")})
	if err != nil {
		t.Fatal(err)
	}
	f, ok := out.Plan.Get("api/internal/handlers/notes_test.go")
	if !ok {
		t.Fatal("no generated handler test")
	}
	test := string(f.Data)
	for _, want := range []string{
		"testkit.DecodeJSON[struct {\n\t\tItems []audit.Event `json:\"items\"`\n\t}](t, rec).Items",
		`if event.Table != "notes" || event.RecordID != strconv.FormatInt(created.Id, 10) {`,
	} {
		if !strings.Contains(test, want) {
			t.Errorf("the generated handler test lacks %q", want)
		}
	}
	schema, ok := out.Plan.Get(auditSchemasPath)
	if !ok || !strings.Contains(string(schema.Data), "\n    table_name:\n") || strings.Contains(string(schema.Data), "\n    table:\n") {
		t.Fatalf("audit schema = %s", schema.Data)
	}
}
