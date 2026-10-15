package handler

import (
	"context"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/stopyellingatme/spinup/internal/gen"
	"github.com/stopyellingatme/spinup/internal/gen/registry"
	"github.com/stopyellingatme/spinup/internal/manifest"
	"github.com/stopyellingatme/spinup/internal/vfs"
)

func init() {
	registry.Register(gen.Generator{
		Name:  "handler",
		Short: "Generate a plain HTTP handler and its test",
		Usage: "<name>",
		Run:   run,
	})
}

var (
	snakeRE     = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	camelBreak  = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	moduleLine  = regexp.MustCompile(`(?m)^module\s+"?([^"\s]+)"?\s*$`)
	goReserved  = map[string]bool{"init": true, "main": true}
	reservedErr = "Try: choose a name such as ping or health_report"
)

// snake converts a snake, kebab, or camel case name to snake case.
func snake(name string) (string, error) {
	s := strings.ToLower(camelBreak.ReplaceAllString(strings.ReplaceAll(name, "-", "_"), "${1}_${2}"))
	if !snakeRE.MatchString(s) || goReserved[s] {
		return "", fmt.Errorf("handler name %q is not valid: use letters, digits, and underscores, and start with a letter\n%s", name, reservedErr)
	}
	return s, nil
}

func pascal(snake string) string {
	var b strings.Builder
	for _, w := range strings.Split(snake, "_") {
		b.WriteString(strings.ToUpper(w[:1]) + w[1:])
	}
	return b.String()
}

const handlerSrc = `package handlers

import (
	"net/http"

	"github.com/stopyellingatme/spinup/kit/httpx"
)

// NAME handles GET ROUTE. Register it in Register in
// api/internal/routes/routes.go:
//
//	mux.HandleFunc("GET ROUTE", handlers.NAME)
func NAME(w http.ResponseWriter, r *http.Request) {
	// Replace this response with the work of the handler.
	if err := httpx.WriteJSON(w, http.StatusOK, map[string]string{"message": "SNAKE"}); err != nil {
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusInternalServerError})
	}
}
`

const testSrc = `package handlers_test

import (
	"net/http"
	"testing"

	"MODULE/internal/handlers"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

func TestNAME(t *testing.T) {
	t.Parallel()
	rec := testkit.Do(t, http.HandlerFunc(handlers.NAME), http.MethodGet, "ROUTE", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET ROUTE: status %d, want %d\n%s", rec.Code, http.StatusOK, rec.Body)
	}
	got := testkit.DecodeJSON[map[string]string](t, rec)
	if got["message"] != "SNAKE" {
		t.Fatalf("GET ROUTE: message %q, want %q", got["message"], "SNAKE")
	}
}
`

func run(_ context.Context, in gen.Input) (*gen.Output, error) {
	if len(in.Args) != 1 {
		return nil, fmt.Errorf("expected one handler name\nTry: spinup g handler ping")
	}
	name, err := snake(in.Args[0])
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(in.Root, "api", "go.mod"))
	if err != nil {
		return nil, fmt.Errorf("read api/go.mod: %w\nTry: run the generator in a spinup project with an API", err)
	}
	m := moduleLine.FindSubmatch(data)
	if m == nil {
		return nil, fmt.Errorf("api/go.mod has no module line\nTry: add a module line to api/go.mod")
	}
	module := string(m[1])
	route := "/api/" + name
	replace := strings.NewReplacer("MODULE", module, "NAME", pascal(name), "ROUTE", route, "SNAKE", name)
	var plan vfs.Plan
	for path, src := range map[string]string{
		"api/internal/handlers/" + name + ".go":      handlerSrc,
		"api/internal/handlers/" + name + "_test.go": testSrc,
	} {
		formatted, err := format.Source([]byte(replace.Replace(src)))
		if err != nil {
			return nil, fmt.Errorf("format %s: %w\nTry: report this generator bug", path, err)
		}
		if err := plan.Add(vfs.File{Path: path, Data: formatted, Class: string(manifest.Scaffold)}); err != nil {
			return nil, err
		}
	}
	return &gen.Output{
		Plan:      plan.Sorted(),
		PostSteps: []gen.PostStep{gen.PostGofmt},
		Next: []string{
			fmt.Sprintf(`add mux.HandleFunc("GET %s", handlers.%s) to Register in api/internal/routes/routes.go, and import %q`, route, pascal(name), module+"/internal/handlers"),
			"cd api && go test ./internal/handlers/...",
		},
	}, nil
}
