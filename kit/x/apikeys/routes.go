package apikeys

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/httpx"
)

type planJSON struct {
	Name      string `json:"name"`
	PerMinute int    `json:"per_minute"`
	PerDay    int    `json:"per_day"`
}

func planView(name string, p Plan) planJSON { return planJSON{name, p.PerMinute, p.PerDay} }

// Routes registers the session-only key and admin routes.
func (s *Store) Routes(a *auth.Auth) func(*http.ServeMux) {
	return func(m *http.ServeMux) {
		register := func(pattern string, guard func(http.Handler) http.Handler, h http.HandlerFunc) {
			m.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				guard(h).ServeHTTP(w, r)
			}))
		}
		register("GET /api/me/keys", a.RequireUser, s.getKeys)
		register("POST /api/me/keys", a.RequireUser, s.createKey)
		register("DELETE /api/me/keys/{id}", a.RequireUser, func(w http.ResponseWriter, r *http.Request) {
			u, _ := auth.UserFrom(r.Context())
			s.revokeRoute(w, r, u.ID)
		})
		admin := a.RequireRole(a.Roles().Admin)
		register("GET /api/admin/apikeys", admin, s.getAccounts)
		register("PUT /api/admin/apikeys/users/{email}/plan", admin, s.setPlanRoute)
		register("DELETE /api/admin/apikeys/keys/{id}", admin, func(w http.ResponseWriter, r *http.Request) { s.revokeRoute(w, r, 0) })
	}
}
func decodeBody(w http.ResponseWriter, r *http.Request, dst any, required, known []string) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		problem(w, r, 415, "The request body must be application/json. Try: send JSON.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := httpx.CheckJSONBody(r, httpx.BodyRules{Required: required, Known: known}); err != nil {
		httpx.WriteProblem(w, r, httpx.ProblemFrom(err))
		return false
	}
	if err := httpx.DecodeJSON(r, dst); err != nil {
		httpx.WriteProblem(w, r, httpx.ProblemFrom(err))
		return false
	}
	return true
}
func (s *Store) localToday(id int64) int64 {
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	if d := s.days[userDay{id, utcDay(s.now())}]; d != nil {
		return d.local
	}
	return 0
}
func (s *Store) getKeys(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	ctx := r.Context()
	name, p, err := s.PlanOf(ctx, u.ID)
	if err != nil {
		s.routeError(w, r, err)
		return
	}
	keys, err := s.List(ctx, u.ID)
	if err != nil {
		s.routeError(w, r, err)
		return
	}
	s.flushMu.Lock()
	usage, err := s.Usage(ctx, u.ID, 30)
	if err != nil {
		s.flushMu.Unlock()
		s.routeError(w, r, err)
		return
	}
	if usage == nil {
		usage = []UsageDay{}
	}
	var today int64
	day := utcDay(s.now()).Format("2006-01-02")
	for _, v := range usage {
		if v.Day == day {
			today += v.Count
		}
	}
	today += s.localToday(u.ID)
	s.flushMu.Unlock()
	_ = httpx.WriteJSON(w, 200, struct {
		Plan    planJSON   `json:"plan"`
		MaxKeys int        `json:"max_keys"`
		Today   int64      `json:"today"`
		Keys    []Key      `json:"keys"`
		Usage   []UsageDay `json:"usage"`
	}{planView(name, p), s.cfg.MaxKeys, today, keys, usage})
}
func (s *Store) createKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name  string `json:"name"`
		Scope Scope  `json:"scope"`
		Days  int    `json:"expires_in_days"`
	}
	if !decodeBody(w, r, &in, []string{"name", "scope"}, []string{"name", "scope", "expires_in_days"}) {
		return
	}
	var fields []httpx.FieldError
	if validateCreate(CreateInput{Name: in.Name, Scope: ScopeRead}) != nil {
		fields = append(fields, httpx.FieldError{Field: "name", Message: "Use 1 to 64 characters without a line break."})
	}
	if !in.Scope.Valid() {
		fields = append(fields, httpx.FieldError{Field: "scope", Message: "Use read or write."})
	}
	if in.Days < 0 || in.Days > 365 {
		fields = append(fields, httpx.FieldError{Field: "expires_in_days", Message: "Use 0 to 365 days."})
	}
	if len(fields) > 0 {
		httpx.WriteProblem(w, r, httpx.Problem{Status: 422, Detail: "The request has invalid fields. Try: correct the fields.", Errors: fields})
		return
	}
	u, _ := auth.UserFrom(r.Context())
	k, token, err := s.Create(r.Context(), u.ID, CreateInput{Name: in.Name, Scope: in.Scope, ExpiresIn: time.Duration(in.Days) * 24 * time.Hour})
	if err != nil {
		s.routeError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, 201, struct {
		Key   Key    `json:"key"`
		Token string `json:"token"`
	}{k, token})
}
func (s *Store) revokeRoute(w http.ResponseWriter, r *http.Request, userID int64) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.routeError(w, r, ErrKeyNotFound)
		return
	}
	if err = s.Revoke(r.Context(), userID, id); err != nil {
		s.routeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Store) getAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.Accounts(r.Context())
	if err != nil {
		s.routeError(w, r, err)
		return
	}
	plans := []planJSON{}
	for _, name := range s.cfg.PlanNames() {
		plans = append(plans, planView(name, s.cfg.Plans[name]))
	}
	_ = httpx.WriteJSON(w, 200, struct {
		Default  string     `json:"default_plan"`
		Plans    []planJSON `json:"plans"`
		Accounts []Account  `json:"accounts"`
	}{s.cfg.DefaultPlan, plans, accounts})
}
func (s *Store) setPlanRoute(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Plan string `json:"plan"`
	}
	if !decodeBody(w, r, &in, []string{"plan"}, []string{"plan"}) {
		return
	}
	if err := s.SetPlan(r.Context(), r.PathValue("email"), in.Plan); err != nil {
		s.routeError(w, r, err)
		return
	}
	accounts, err := s.Accounts(r.Context())
	if err != nil {
		s.routeError(w, r, err)
		return
	}
	for _, a := range accounts {
		if a.Email == r.PathValue("email") || a.Email == strings.ToLower(strings.TrimSpace(r.PathValue("email"))) {
			_ = httpx.WriteJSON(w, 200, a)
			return
		}
	}
	s.routeError(w, r, ErrUserNotFound)
}
func (s *Store) routeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrTooManyKeys):
		problem(w, r, 422, fmt.Sprintf("You have the maximum of %d keys. Try: revoke a key first.", s.cfg.MaxKeys))
	case errors.Is(err, ErrUnknownPlan):
		httpx.WriteProblem(w, r, httpx.Problem{Status: 422, Detail: "The plan is not declared. Try: select a declared plan.", Errors: []httpx.FieldError{{Field: "plan", Message: "Select a declared plan."}}})
	case errors.Is(err, ErrKeyNotFound), errors.Is(err, ErrUserNotFound):
		problem(w, r, 404, "The requested item was not found. Try: check the ID or email.")
	default:
		s.logger.ErrorContext(r.Context(), "apikeys: route failed", "error", err)
		problem(w, r, 500, "The request failed. Try: check the server logs.")
	}
}
