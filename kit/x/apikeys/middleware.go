package apikeys

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/kit/audit"
	"github.com/stopyellingatme/spinup/kit/httpx"
)

func problem(w http.ResponseWriter, r *http.Request, status int, detail string) {
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteProblem(w, r, httpx.Problem{Status: status, Detail: detail})
}

func quotaHeaders(w http.ResponseWriter, plan Plan, used int64, now time.Time) {
	w.Header().Set("RateLimit-Limit", strconv.Itoa(plan.PerDay))
	w.Header().Set("RateLimit-Remaining", strconv.FormatInt(max(0, int64(plan.PerDay)-used), 10))
	w.Header().Set("RateLimit-Reset", strconv.FormatInt(midnightSeconds(now), 10))
}

// Middleware enforces key scope and quotas. Install it after the auth middleware.
// Requests without an API key context pass through unchanged.
func (s *Store) Middleware() httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			req, ok := KeyFrom(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			now := s.now()
			who := userDay{req.Key.UserID, utcDay(now)}
			quotaHeaders(w, req.Plan, s.cachedUsed(who), now)
			if strings.HasPrefix(r.URL.Path, "/api/me/keys") || strings.HasPrefix(r.URL.Path, "/api/admin/") {
				s.record(r.Context(), req.PlanName, "forbidden")
				problem(w, r, http.StatusForbidden, "An API key cannot manage keys or call admin routes.")
				return
			}
			rpc := s.rpcPath != "" && r.Method == http.MethodPost && r.URL.Path == s.rpcPath
			if req.Key.Scope == ScopeRead && r.Method != http.MethodGet && r.Method != http.MethodHead && !rpc {
				s.record(r.Context(), req.PlanName, "forbidden")
				problem(w, r, http.StatusForbidden, "This API key is read-only. Try: create a key with the write scope.")
				return
			}
			s.quotaMu.Lock()
			wait := s.takeMinute(req.Key.UserID, req.Plan.PerMinute, now)
			if wait > 0 {
				s.quotaMu.Unlock()
				seconds := max(1, int64((wait+time.Second-1)/time.Second))
				w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
				s.record(r.Context(), req.PlanName, "minute_limit")
				problem(w, r, http.StatusTooManyRequests, fmt.Sprintf("The %s plan allows %d requests a minute. Try again in %d seconds.", req.PlanName, req.Plan.PerMinute, seconds))
				return
			}
			d := s.days[who]
			if d == nil {
				s.quotaMu.Unlock()
				var base int64
				err := s.pool.QueryRow(r.Context(), totalSQL, who.userID, who.day).Scan(&base)
				s.quotaMu.Lock()
				if err != nil {
					s.quotaMu.Unlock()
					s.logger.ErrorContext(r.Context(), "apikeys: cannot check the daily quota", "error", storeFailure("read today's usage", err))
					problem(w, r, http.StatusServiceUnavailable, "The API key quota is unavailable. Try: retry the request later.")
					return
				}
				// Another request may have initialized the day during the query.
				d = s.days[who]
				if d == nil {
					d = &dayUsage{base: base}
					s.days[who] = d
				}
			}
			used := d.base + d.local
			if used >= int64(req.Plan.PerDay) {
				s.quotaMu.Unlock()
				quotaHeaders(w, req.Plan, used, now)
				w.Header().Set("Retry-After", strconv.FormatInt(midnightSeconds(now), 10))
				s.record(r.Context(), req.PlanName, "day_limit")
				problem(w, r, http.StatusTooManyRequests, fmt.Sprintf("The %s plan allows %d requests a day. Try again after midnight UTC.", req.PlanName, req.Plan.PerDay))
				return
			}
			d.local++
			s.pending[keyDay{req.Key.ID, req.Key.UserID, who.day}]++
			s.quotaMu.Unlock()
			quotaHeaders(w, req.Plan, used+1, now)
			ctx := r.Context()
			if actor, ok := audit.ActorFrom(ctx); ok {
				actor.Reason = "api_key:" + strconv.FormatInt(req.Key.ID, 10)
				ctx = audit.WithActor(ctx, actor)
			}
			s.record(ctx, req.PlanName, "ok")
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
