package resource

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/gen"
	genrun "github.com/stopyellingatme/spinup/internal/gen/run"
	"github.com/stopyellingatme/spinup/internal/manifest"
)

// legacyValidation is the exact validation.go generated before ADR 0027.
const legacyValidation = `package handlers

import (
	"net/http"

	"github.com/stopyellingatme/spinup/kit/httpx"
)

// requiredFields maps ServeMux patterns to required JSON properties.
// Each resource registers its POST and PUT patterns during initialization.
var requiredFields = map[string][]string{}

// RequireFields validates required properties before the strict JSON decoder.
func RequireFields(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := httpx.RequireJSONFields(r, requiredFields[r.Pattern]); err != nil {
			httpx.WriteProblem(w, r, httpx.ProblemFrom(err))
			return
		}
		next.ServeHTTP(w, r)
	})
}
`

func TestValidationCompatibility(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, source string
		update       bool
	}{
		{"legacy", legacyValidation, true},
		{"current", strings.TrimSpace(requiredFieldsGo) + "\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := freshApp(t)
			write(t, root, map[string]string{requiredFieldsPath: tc.source})
			lock := manifest.New()
			if err := lock.SetFile(requiredFieldsPath, manifest.FileEntry{Class: manifest.Scaffold, Template: "gen/resource", Hash: manifest.Hash([]byte(tc.source)), Mode: "0644"}); err != nil {
				t.Fatal(err)
			}
			route, err := os.ReadFile(filepath.Join(root, routesGenPath))
			if err != nil {
				t.Fatal(err)
			}
			if err := lock.SetFile(routesGenPath, manifest.FileEntry{Class: manifest.Managed, Template: "gen/resource", Hash: manifest.Hash(route), Mode: "0644"}); err != nil {
				t.Fatal(err)
			}
			if err := lock.Save(filepath.Join(root, "spinup.lock")); err != nil {
				t.Fatal(err)
			}
			out, err := run(t.Context(), gen.Input{Root: root, Args: []string{"note", "title:string?"}, Flags: flagSet(t, "--api-only")})
			if err != nil {
				t.Fatal(err)
			}
			file, ok := out.Plan.Get(requiredFieldsPath)
			if ok != tc.update {
				t.Fatalf("validation replacement = %t, want %t", ok, tc.update)
			}
			if tc.update {
				want, err := withModule(requiredFieldsGo, "")
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(file.Data, want) {
					t.Fatalf("replacement = %s, want %s", file.Data, want)
				}
				if !slices.Contains(out.Edits, requiredFieldsPath) {
					t.Fatal("legacy replacement needs an authorized edit")
				}
			}
			if _, err := genrun.Run(context.Background(), gen.Generator{Name: "resource", Run: run}, []string{"note", "title:string?"}, flagSet(t, "--api-only"), genrun.Options{Root: root, DryRun: true}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(root, requiredFieldsPath))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.source {
				t.Fatal("planning changed validation.go")
			}
		})
	}
}

func TestEditedValidationRefusesResource(t *testing.T) {
	t.Parallel()
	for _, force := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "force"}[force], func(t *testing.T) {
			t.Parallel()
			root := freshApp(t)
			source := legacyValidation + "// User customization.\n"
			write(t, root, map[string]string{requiredFieldsPath: source})
			var flags []string
			if force {
				flags = append(flags, "--force")
			}
			out, err := run(t.Context(), gen.Input{Root: root, Args: []string{"note", "title:string"}, Flags: flagSet(t, flags...)})
			if err == nil || out != nil {
				t.Fatalf("has output = %t, error = %v, want refusal", out != nil, err)
			}
			if last := strings.Split(strings.TrimSpace(err.Error()), "\n"); !strings.HasPrefix(last[len(last)-1], "Try:") {
				t.Fatalf("error does not end with a Try line: %s", err)
			}
			for _, want := range []string{requiredFieldsPath, "differ", "Try:", "var knownFields = map[string][]string{}", "httpx.MaxBody(maxJSONBody)", "httpx.CheckJSONBody"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %s, want %s", err, want)
				}
			}
			got, err := os.ReadFile(filepath.Join(root, requiredFieldsPath))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != source {
				t.Fatal("planning changed customized validation")
			}
			if _, err := os.Stat(filepath.Join(root, "api/internal/handlers/notes.go")); !os.IsNotExist(err) {
				t.Fatalf("resource handler exists: %v", err)
			}
		})
	}
}
