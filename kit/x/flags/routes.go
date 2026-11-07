package flags

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/httpx"
)

// Routes registers the public and role-guarded flag routes. The routes need both middlewares.
func (s *Store) Routes(a *auth.Auth) func(*http.ServeMux) {
	return func(mux *http.ServeMux) {
		roles := a.Roles()
		guard := a.RequireRole(roles.Admin)
		mux.HandleFunc("GET /api/flags", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			declared := s.flags.Declared()
			values := make(map[string]bool)
			for _, key := range declared.Keys() {
				if declared[key].Public {
					values[key] = Evaluate(r.Context(), key).Value
				}
			}
			_ = httpx.WriteJSON(w, http.StatusOK, struct {
				Flags map[string]bool `json:"flags"`
			}{values})
		})
		admin := func(pattern string, h http.HandlerFunc) {
			mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				guard(h).ServeHTTP(w, r)
			}))
		}
		admin("GET /api/admin/flags", func(w http.ResponseWriter, r *http.Request) {
			entries, err := s.List(r.Context(), time.Now())
			if err != nil {
				s.routeError(w, r, err, nil)
				return
			}
			_ = httpx.WriteJSON(w, http.StatusOK, struct {
				Flags     []Entry  `json:"flags"`
				Roles     []string `json:"roles"`
				AdminRole string   `json:"admin_role"`
			}{entries, roles.Names, roles.Admin})
		})
		admin("PUT /api/admin/flags/{key}", func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Enabled    bool     `json:"enabled"`
				Roles      []string `json:"roles"`
				Percentage int      `json:"percentage"`
			}
			if !decodeFlagBody(w, r, &in, []string{"enabled", "roles", "percentage"}) {
				return
			}
			err := s.SetRule(r.Context(), r.PathValue("key"), Rule{Enabled: in.Enabled, Roles: in.Roles, Percentage: in.Percentage})
			if err != nil {
				s.routeError(w, r, err, in.Roles)
				return
			}
			s.writeEntry(w, r)
		})
		admin("PUT /api/admin/flags/{key}/users/{email}", func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Enabled bool `json:"enabled"`
			}
			if !decodeFlagBody(w, r, &in, []string{"enabled"}) {
				return
			}
			if err := s.SetUser(r.Context(), r.PathValue("key"), r.PathValue("email"), in.Enabled); err != nil {
				s.routeError(w, r, err, nil)
				return
			}
			s.writeEntry(w, r)
		})
		admin("DELETE /api/admin/flags/{key}/users/{email}", func(w http.ResponseWriter, r *http.Request) {
			err := s.ClearUser(r.Context(), r.PathValue("key"), r.PathValue("email"))
			if err != nil && !errors.Is(err, ErrUserNotFound) {
				s.routeError(w, r, err, nil)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
}

func decodeFlagBody(w http.ResponseWriter, r *http.Request, dst any, fields []string) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusUnsupportedMediaType, Detail: "The request body must be application/json."})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := httpx.CheckJSONBody(r, httpx.BodyRules{Required: fields, Known: fields}); err != nil {
		httpx.WriteProblem(w, r, httpx.ProblemFrom(err))
		return false
	}
	if err := httpx.DecodeJSON(r, dst); err != nil {
		httpx.WriteProblem(w, r, httpx.ProblemFrom(err))
		return false
	}
	return true
}

func (s *Store) writeEntry(w http.ResponseWriter, r *http.Request) {
	entries, err := s.List(r.Context(), time.Now())
	if err != nil {
		s.routeError(w, r, err, nil)
		return
	}
	for _, entry := range entries {
		if entry.Key == r.PathValue("key") {
			_ = httpx.WriteJSON(w, http.StatusOK, entry)
			return
		}
	}
	s.routeError(w, r, ErrUnknownKey, nil)
}

func (s *Store) routeError(w http.ResponseWriter, r *http.Request, err error, roles []string) {
	p := httpx.Problem{Status: http.StatusUnprocessableEntity, Detail: "The request has invalid fields."}
	switch {
	case errors.Is(err, ErrUnknownKey):
		key := r.PathValue("key")
		p = httpx.Problem{Status: http.StatusNotFound, Detail: fmt.Sprintf("The flag %q is not declared. Try: add [flags.%s] to spinup.toml and run spinup g flags.", key, key)}
	case errors.Is(err, ErrUnknownRole):
		names := slices.Clone(s.roles.Names)
		for _, role := range roles {
			if !s.roles.Valid(role) {
				p.Errors = []httpx.FieldError{{Field: "roles", Message: fmt.Sprintf("The role %q is not declared. Use one of: %s.", role, strings.Join(names, ", "))}}
				break
			}
		}
	case errors.Is(err, ErrPercentage):
		p.Errors = []httpx.FieldError{{Field: "percentage", Message: "Use a whole number from 0 to 100."}}
	case errors.Is(err, ErrUserNotFound):
		p.Errors = []httpx.FieldError{{Field: "email", Message: "No account has this email address."}}
	default:
		s.logger.ErrorContext(r.Context(), "flags: route failed", "error", err, "request_id", httpx.RequestIDFrom(r.Context()))
		p = httpx.Problem{Status: http.StatusInternalServerError}
	}
	httpx.WriteProblem(w, r, p)
}
