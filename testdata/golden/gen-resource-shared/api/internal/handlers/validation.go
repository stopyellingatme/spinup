package handlers

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
