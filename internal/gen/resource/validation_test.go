package resource

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"go.yaml.in/yaml/v3"

	exec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/gen"
	_ "github.com/stopyellingatme/spinup/internal/gen/openapi"
	genrun "github.com/stopyellingatme/spinup/internal/gen/run"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

type offlineValidationRunner struct{}

// Run compiles the generated app against the local kit without network access.
func (offlineValidationRunner) Run(ctx context.Context, cmd exec.Cmd) (exec.Result, error) {
	cmd.Env = append(cmd.Env, "GOWORK=off", "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly -p=4", "GOPROXY=off", "GOSUMDB=off")
	return (exec.OSRunner{}).Run(ctx, cmd)
}

// TestGeneratedJSONBody runs generated handler tests against the local kit.
func TestGeneratedJSONBody(t *testing.T) {
	root := freshApp(t)
	modPath := filepath.Join(root, "api/go.mod")
	mod, err := os.ReadFile(modPath)
	if err != nil {
		t.Fatal(err)
	}
	mod = append(mod, []byte("\nreplace github.com/stopyellingatme/spinup/kit => "+strconv.Quote(filepath.Join(testutil.RepoRoot(t), "kit"))+"\n")...)
	if err := os.WriteFile(modPath, mod, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := manifest.New().Save(filepath.Join(root, "spinup.lock")); err != nil {
		t.Fatal(err)
	}
	write(t, root, map[string]string{
		requiredFieldsPath: legacyValidation,
		"api/internal/handlers/legacy.go": `package handlers

func init() {
    requiredFields["POST /legacy"] = []string{"title"}
    requiredFields["PUT /legacy"] = []string{}
    knownFields["PATCH /known"] = []string{"title"}
}
`,
		"api/internal/handlers/legacy_test.go": legacyJSONBodyTest,
	})
	runner := offlineValidationRunner{}
	for _, args := range [][]string{{"note", "zeta:string", "alpha:bool?"}, {"widget", "value:string?"}} {
		if _, err := genrun.Run(t.Context(), gen.Generator{Name: "resource", Run: run}, args, flagSet(t, "--api-only"), genrun.Options{Root: root, Runner: runner, Force: true}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := runner.Run(t.Context(), exec.Cmd{
		Name: "go", Args: []string{"test", "-race", "./internal/handlers", "-run", "RejectsBadInput|RequiresFields|ChecksJSONBody|LegacyJSONBody", "-count=1", "-v"},
		Dir: filepath.Join(root, "api"),
	})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("generated handler tests: %v\n%s%s", err, result.Stdout, result.Stderr)
	}
	t.Logf("%s", result.Stdout)
}

// TestBodyRulesMatchSchema checks writable fields, sorting, and route registration.
func TestBodyRulesMatchSchema(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		args  []string
		owned bool
	}{
		{"required", []string{"note", "zeta:string", "alpha:bool", "parent:ref:note?"}, false},
		{"nullable", []string{"note", "zeta:string?", "alpha:bool?"}, false},
		{"owned", []string{"note", "zeta:string", "alpha:bool?"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec, err := ParseSpec(t.TempDir(), tc.args, "resource")
			if err != nil {
				t.Fatal(err)
			}
			spec.Owned = tc.owned
			source, err := newResourceCode(spec, "example.com/todo/api", "").handlersGo()
			if err != nil {
				t.Fatal(err)
			}
			file, err := parser.ParseFile(token.NewFileSet(), "notes.go", source, 0)
			if err != nil {
				t.Fatal(err)
			}
			rules := map[string][]string{}
			patterns := map[string][]string{}
			ast.Inspect(file, func(node ast.Node) bool {
				assign, ok := node.(*ast.AssignStmt)
				if !ok {
					return true
				}
				switch lhs := assign.Lhs[0].(type) {
				case *ast.Ident:
					if lhs.Name != "required" && lhs.Name != "known" {
						return true
					}
					rules[lhs.Name] = nil
					for _, value := range assign.Rhs[0].(*ast.CompositeLit).Elts {
						text, err := strconv.Unquote(value.(*ast.BasicLit).Value)
						if err != nil {
							t.Fatal(err)
						}
						rules[lhs.Name] = append(rules[lhs.Name], text)
					}
				case *ast.IndexExpr:
					name, ok := lhs.X.(*ast.Ident)
					if !ok || (name.Name != "requiredFields" && name.Name != "knownFields") {
						return true
					}
					pattern, err := strconv.Unquote(lhs.Index.(*ast.BasicLit).Value)
					if err != nil {
						t.Fatal(err)
					}
					patterns[name.Name] = append(patterns[name.Name], pattern)
				}
				return true
			})
			var schemas map[string]schemaObject
			if err := yaml.Unmarshal(schemasYAML(spec), &schemas); err != nil {
				t.Fatal(err)
			}
			input := schemas["NoteInput"]
			slices.Sort(input.Required)
			want := map[string][]string{"known": propertyNames(input), "required": input.Required}
			if !reflect.DeepEqual(rules, want) {
				t.Fatalf("rules = %v, want %v", rules, want)
			}
			for _, name := range []string{"requiredFields", "knownFields"} {
				if want := []string{"POST /api/notes", "PUT /api/notes/{id}"}; !slices.Equal(patterns[name], want) {
					t.Fatalf("%s patterns = %v, want %v", name, patterns[name], want)
				}
			}
		})
	}
}

// legacyJSONBodyTest compiles unchanged registrations against migrated middleware.
const legacyJSONBodyTest = `package handlers

import (
    "io"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"
)

func TestLegacyJSONBody(t *testing.T) {
    t.Parallel()
    valid := "{\"title\":\"hello\",\"extra\":true}"
    tests := []struct {name,pattern,body string; status int}{
        {"legacy accepts unknown", "POST /legacy", valid, http.StatusNoContent},
        {"legacy requires field", "POST /legacy", "{}", http.StatusUnprocessableEntity},
        {"legacy duplicate", "POST /legacy", "{\"title\":1,\"title\":2}", http.StatusBadRequest},
        {"legacy invalid UTF-8", "POST /legacy", "{\"title\":\"\xff\"}", http.StatusBadRequest},
        {"legacy oversized", "POST /legacy", valid+strings.Repeat(" ",1<<20), http.StatusRequestEntityTooLarge},
        {"empty required duplicate", "PUT /legacy", "{\"extra\":1,\"extra\":2}", http.StatusBadRequest},
        {"known only valid", "PATCH /known", "{\"title\":\"hello\"}", http.StatusNoContent},
        {"known only unknown", "PATCH /known", valid, http.StatusBadRequest},
    }
    for _,tc := range tests {
        for _,unknownLength := range []bool{false,true} {
            t.Run(tc.name+map[bool]string{false:"/length",true:"/chunked"}[unknownLength],func(t *testing.T){
                t.Parallel()
                called := false
                mux := http.NewServeMux()
                mux.Handle(tc.pattern,RequireFields(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
                    called = true
                    data,err := io.ReadAll(r.Body)
                    if err != nil { t.Fatal(err) }
                    if string(data) != tc.body { t.Fatal("middleware changed body") }
                    w.WriteHeader(http.StatusNoContent)
                })))
                method,path,_ := strings.Cut(tc.pattern," ")
                req := httptest.NewRequest(method,path,strings.NewReader(tc.body))
                if unknownLength { req.ContentLength = -1; req.TransferEncoding = []string{"chunked"} }
                rec := httptest.NewRecorder()
                mux.ServeHTTP(rec,req)
                if rec.Code != tc.status { t.Fatalf("status %d, want %d: %s",rec.Code,tc.status,rec.Body) }
                if called != (tc.status == http.StatusNoContent) { t.Fatalf("handler called = %t",called) }
            })
        }
    }
}
`
