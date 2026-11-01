package handlers

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
