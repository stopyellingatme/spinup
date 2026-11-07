package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/httpx"
)

// ErrUserNotFound means no user has the requested ID or email.
var ErrUserNotFound = errors.New("billing: the user was not found.\nTry: run user list and use an existing account")

// PlanRequiredType is the problem type of the 402 answer of Require.
const PlanRequiredType = "plan_required"

// Entitlement returns the effective plan of the user. A user with no
// entitlement row has the default plan. The features come from the current
// Config, so a changed declaration applies at once.
func (s *Service) Entitlement(ctx context.Context, userID int64) (Entitlement, error) {
	var plan, status *string
	var e Entitlement
	var periodEnd *time.Time
	err := s.pool.QueryRow(ctx, `SELECT e.plan, e.status, c.user_id IS NOT NULL, sub.current_period_end, COALESCE(sub.cancel_at_period_end, false)
FROM users u
LEFT JOIN billing_entitlements e ON e.user_id = u.id
LEFT JOIN billing_customers c ON c.user_id = u.id
LEFT JOIN LATERAL (SELECT current_period_end, cancel_at_period_end FROM billing_subscriptions
  WHERE user_id = u.id AND plan = e.plan AND status = e.status
  ORDER BY current_period_end DESC NULLS LAST, id LIMIT 1) sub ON true
WHERE u.id = $1`, userID).Scan(&plan, &status, &e.HasCustomer, &periodEnd, &e.CancelAtPeriodEnd)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entitlement{}, ErrUserNotFound
	}
	if err != nil {
		return Entitlement{}, fmt.Errorf("billing: cannot read the entitlement: %w\nTry: check the database connection and run spinup db migrate up, then retry", err)
	}
	e.Plan = s.cfg.DefaultPlan
	if plan != nil {
		if _, ok := s.cfg.Plan(*plan); ok {
			e.Plan = *plan
		} else {
			s.warnOnce(ctx, "plan:"+*plan, "billing: a stored plan has no declaration, so the user has the default plan. Try: declare the plan or run billing sync.", "plan", *plan)
		}
	}
	if status != nil && e.Plan == *plan {
		e.Status = *status
	}
	if periodEnd != nil && e.Status != "" {
		e.CurrentPeriodEnd = periodEnd.UTC()
	} else {
		e.CancelAtPeriodEnd = false
	}
	p, _ := s.cfg.Plan(e.Plan)
	e.Features = nonNil(slices.Clone(p.Features))
	return e, nil
}

type entitlementKey struct{}

type cachedEntitlement struct {
	userID int64
	e      Entitlement
}

// entitlementFor returns the entitlement of the user, from the request
// context when Require already read it.
func (s *Service) entitlementFor(ctx context.Context, userID int64) (Entitlement, error) {
	if c, ok := ctx.Value(entitlementKey{}).(cachedEntitlement); ok && c.userID == userID {
		return c.e, nil
	}
	return s.Entitlement(ctx, userID)
}

// Has reports whether the signed-in user of ctx has the feature. Without a
// user it returns false.
func (s *Service) Has(ctx context.Context, feature string) (bool, error) {
	u, ok := auth.UserFrom(ctx)
	if !ok {
		return false, nil
	}
	e, err := s.entitlementFor(ctx, u.ID)
	if err != nil {
		return false, err
	}
	return slices.Contains(e.Features, feature), nil
}

// Require answers 401 without a signed-in user, and 402 with the problem type
// plan_required when the plan lacks a listed feature. It keeps the
// entitlement in the request context, so a request reads it once.
func (s *Service) Require(features ...string) httpx.Middleware {
	features = slices.Clone(features)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			u, ok := auth.UserFrom(ctx)
			if !ok {
				problem(w, r, http.StatusUnauthorized, "No user is signed in. Try: sign in.")
				return
			}
			e, err := s.entitlementFor(ctx, u.ID)
			if err != nil {
				s.logger.ErrorContext(ctx, "billing: cannot read the entitlement", "error", err, "request_id", httpx.RequestIDFrom(ctx))
				problem(w, r, http.StatusInternalServerError, "The plan could not be read. Try: retry later. Check the server logs.")
				return
			}
			for _, f := range features {
				if !slices.Contains(e.Features, f) {
					planRequired(w, r, f)
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, entitlementKey{}, cachedEntitlement{u.ID, e})))
		})
	}
}

// planRequired writes the 402 problem with the missing feature.
func planRequired(w http.ResponseWriter, r *http.Request, feature string) {
	body := struct {
		httpx.Problem
		Feature   string `json:"feature"`
		RequestID string `json:"request_id,omitempty"`
	}{httpx.Problem{Type: PlanRequiredType, Title: "Plan required", Status: http.StatusPaymentRequired,
		Detail: fmt.Sprintf("Your plan does not include the feature %q. Try: upgrade the plan on /pricing.", feature)}, feature, httpx.RequestIDFrom(r.Context())}
	data, _ := json.Marshal(body)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusPaymentRequired)
	_, _ = w.Write(append(data, '\n'))
}
