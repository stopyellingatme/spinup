// Package validation shares generated JSON middleware with its upgrade codemod.
package validation

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"

	"github.com/stopyellingatme/spinup/internal/codemod"
)

// Path is the generated middleware path, relative to the application root.
const Path = "api/internal/handlers/validation.go"

// Source is the current middleware shared by generated resource handlers.
const Source = `package handlers

import (
	"net/http"

	"github.com/stopyellingatme/spinup/kit/httpx"
)

// maxJSONBody limits generated JSON requests to 1 MiB.
const maxJSONBody = 1 << 20

// requiredFields maps write patterns to their required JSON properties.
// Keep this type compatible with previously generated resources.
var requiredFields = map[string][]string{}

// knownFields maps write patterns to their allowed JSON properties.
// Each new resource registers every write pattern in both maps.
var knownFields = map[string][]string{}

// RequireFields limits and validates JSON bodies before the strict JSON decoder.
func RequireFields(next http.Handler) http.Handler {
	return httpx.MaxBody(maxJSONBody)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, required := requiredFields[r.Pattern]
		_, known := knownFields[r.Pattern]
		if required || known {
			rules := httpx.BodyRules{Required: requiredFields[r.Pattern], Known: knownFields[r.Pattern]}
			if err := httpx.CheckJSONBody(r, rules); err != nil {
				httpx.WriteProblem(w, r, httpx.ProblemFrom(err))
				return
			}
		}
		next.ServeHTTP(w, r)
	}))
}
`

// LegacySource is the exact middleware generated before ADR 0027.
const LegacySource = `package handlers

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

// Upgrade replaces only a known generated version and preserves current bytes.
// A customized file requires a manual change before generation or upgrade.
func Upgrade(src []byte) ([]byte, bool, error) {
	if bytes.Equal(src, []byte(Source)) {
		return src, false, nil
	}
	if !bytes.Equal(src, []byte(LegacySource)) {
		return nil, false, fmt.Errorf("cannot update %s: its contents differ from the known generated versions\n%s\nTry: replace %s with the code above to add knownFields and the new middleware", Path, Source, Path)
	}
	return codemod.EditGo(Path, src, func(fset *token.FileSet, file *ast.File) (bool, error) {
		replacement, err := parser.ParseFile(fset, Path, Source, parser.ParseComments)
		if err != nil {
			return false, err
		}
		*file = *replacement
		return true, nil
	})
}
