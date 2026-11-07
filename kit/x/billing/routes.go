package billing

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stopyellingatme/spinup/kit/audit"
	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/httpx"
	"github.com/stopyellingatme/spinup/kit/x/apikeys"
	"github.com/stopyellingatme/spinup/kit/x/billing/stripe"
)

// catalogTTL is how long the plans route keeps the Stripe prices.
const catalogTTL = 5 * time.Minute

// bodyLimit is the largest JSON body of a session route.
const bodyLimit = 16 << 10

// Routes registers the session routes, the public plans route, and the
// webhook. Without a jobs client or a webhook secret, it does not register the
// webhook and logs an error once.
func (s *Service) Routes(a *auth.Auth) func(*http.ServeMux) {
	return func(m *http.ServeMux) {
		session := func(pattern string, h http.HandlerFunc) {
			m.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				a.RequireUser(sessionOnly(h)).ServeHTTP(w, r)
			}))
		}
		session("GET "+SessionPrefix, s.getBilling)
		session("POST "+SessionPrefix+"/checkout", s.checkout)
		session("POST "+SessionPrefix+"/portal", s.portal)
		session("POST "+SessionPrefix+"/sync", s.syncRoute)
		m.HandleFunc("GET /api/billing/plans", s.plans)
		if s.jobs == nil || len(s.secrets) == 0 {
			s.webhookOnce.Do(func() {
				s.logger.Error("billing: the webhook route is not registered: it needs a jobs client and a webhook secret. Try: pass billing.WithJobs and billing.WithWebhookSecrets(STRIPE_WEBHOOK_SECRET).",
					"jobs", s.jobs != nil, "secrets", len(s.secrets))
			})
			return
		}
		m.HandleFunc("POST "+WebhookPath, s.webhook)
	}
}

// sessionOnly answers 403 for a request with an API key. The apikeys
// middleware already blocks SessionPrefix; this is defense in depth.
func sessionOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := apikeys.KeyFrom(r.Context()); ok {
			problem(w, r, http.StatusForbidden, "An API key cannot call the billing routes. Try: sign in with a browser session.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// billingJSON is the body of GET /api/me/billing.
type billingJSON struct {
	Plan              string     `json:"plan"`
	Features          []string   `json:"features"`
	Status            string     `json:"status"`
	CurrentPeriodEnd  *time.Time `json:"current_period_end"`
	CancelAtPeriodEnd bool       `json:"cancel_at_period_end"`
	HasCustomer       bool       `json:"has_customer"`
}

func billingView(e Entitlement) billingJSON {
	v := billingJSON{Plan: e.Plan, Features: nonNil(e.Features), Status: e.Status, CancelAtPeriodEnd: e.CancelAtPeriodEnd, HasCustomer: e.HasCustomer}
	if !e.CurrentPeriodEnd.IsZero() {
		t := e.CurrentPeriodEnd
		v.CurrentPeriodEnd = &t
	}
	return v
}

func (s *Service) internal(w http.ResponseWriter, r *http.Request, action string, err error) {
	s.logger.ErrorContext(r.Context(), "billing: cannot "+action, "error", err, "request_id", httpx.RequestIDFrom(r.Context()))
	problem(w, r, http.StatusInternalServerError, "The request failed. Try: retry later. Check the server logs.")
}

func (s *Service) stripeFailed(w http.ResponseWriter, r *http.Request, action string, err error) {
	s.logger.ErrorContext(r.Context(), "billing: Stripe failed to "+action, "error", err, "request_id", httpx.RequestIDFrom(r.Context()))
	problem(w, r, http.StatusBadGateway, "Stripe did not answer the request. Try: retry later.")
}

func (s *Service) getBilling(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	e, err := s.Entitlement(r.Context(), u.ID)
	if err != nil {
		s.internal(w, r, "read the entitlement", err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, billingView(e))
}

// decodeBody reads a strict JSON body (ADR 0027). An empty body is allowed
// when no field is required.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any, required, known []string) bool {
	r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
	if len(required) == 0 && r.ContentLength == 0 && r.Header.Get("Content-Type") == "" {
		return true
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		problem(w, r, http.StatusUnsupportedMediaType, "The request body must be application/json. Try: send JSON.")
		return false
	}
	if err := httpx.CheckJSONBody(r, httpx.BodyRules{Required: required, Known: known}); err != nil {
		w.Header().Set("Cache-Control", "no-store")
		httpx.WriteProblem(w, r, withTry(httpx.ProblemFrom(err)))
		return false
	}
	if err := httpx.DecodeJSON(r, dst); err != nil {
		w.Header().Set("Cache-Control", "no-store")
		httpx.WriteProblem(w, r, withTry(httpx.ProblemFrom(err)))
		return false
	}
	return true
}

// withTry adds a Try: line to a decode problem that has none.
func withTry(p httpx.Problem) httpx.Problem {
	if !strings.Contains(p.Detail, "Try:") {
		p.Detail = strings.TrimSpace(p.Detail + " Try: send a JSON object with the documented fields.")
	}
	return p
}

// customerOf returns the Stripe customer of the user, or "".
func (s *Service) customerOf(ctx context.Context, userID int64) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT customer_id FROM billing_customers WHERE user_id=$1`, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// ensureCustomer returns the Stripe customer of the user and creates it when
// it is missing. The idempotency key customer:<user id> makes a race safe.
func (s *Service) ensureCustomer(ctx context.Context, userID int64) (string, error) {
	id, err := s.customerOf(ctx, userID)
	if err != nil || id != "" {
		return id, err
	}
	var email string
	if err := s.pool.QueryRow(ctx, `SELECT email FROM users WHERE id=$1`, userID).Scan(&email); err != nil {
		return "", err
	}
	user := strconv.FormatInt(userID, 10)
	c, err := s.client.CreateCustomer(ctx, stripe.CustomerParams{Email: email, Metadata: map[string]string{"user_id": user}}, "customer:"+user)
	if err != nil {
		return "", err
	}
	err = audit.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO billing_customers(user_id,customer_id) VALUES($1,$2) ON CONFLICT (user_id) DO NOTHING`, userID, c.ID)
		return err
	})
	if err != nil {
		return "", err
	}
	return s.customerOf(ctx, userID)
}

func (s *Service) hasLiveSubscription(ctx context.Context, userID int64) (bool, error) {
	var found bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM billing_subscriptions WHERE user_id=$1 AND status = ANY($2::text[]))`, userID, liveStatuses).Scan(&found)
	return found, err
}

func (s *Service) checkout(w http.ResponseWriter, r *http.Request) {
	if !s.configured(w, r) {
		return
	}
	var in struct {
		Price string `json:"price"`
	}
	if !decodeBody(w, r, &in, []string{"price"}, []string{"price"}) {
		return
	}
	ctx := r.Context()
	if _, ok := s.cfg.planOfPrice(in.Price); !ok {
		w.Header().Set("Cache-Control", "no-store")
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusBadRequest, Detail: "The price is not a declared plan price. Try: select a price from GET /api/billing/plans.",
			Errors: []httpx.FieldError{{Field: "price", Message: "Use a declared price lookup key."}}})
		return
	}
	u, _ := auth.UserFrom(ctx)
	found, err := s.hasLiveSubscription(ctx, u.ID)
	if err != nil {
		s.internal(w, r, "read the subscriptions", err)
		return
	}
	if found {
		problem(w, r, http.StatusConflict, "You already have a subscription. Try: open the billing portal to change the plan.")
		return
	}
	prices, err := s.catalog(ctx)
	if err != nil {
		s.stripeFailed(w, r, "list the prices", err)
		return
	}
	price, ok := prices[in.Price]
	if !ok {
		s.warnOnce(ctx, "price:"+in.Price, "billing: a declared price lookup key is not an active Stripe price. Try: run billing check.", "lookup_key", in.Price)
		problem(w, r, http.StatusServiceUnavailable, "The price is not available now. Try: retry later, or ask the operator to run billing check.")
		return
	}
	plan, _ := s.cfg.planOfPrice(in.Price)
	chosenMetered := isMetered(price)
	var meteredIDs []string
	licensedInPlan := false
	for _, key := range plan.Prices {
		other, ok := prices[key]
		switch {
		case !ok:
			if key != in.Price {
				s.warnOnce(ctx, "price:"+key, "billing: a declared price lookup key is not an active Stripe price. Try: run billing check.", "lookup_key", key)
			}
		case !isMetered(other):
			licensedInPlan = true
		case key != in.Price:
			meteredIDs = append(meteredIDs, other.ID)
		}
	}
	if chosenMetered && licensedInPlan {
		w.Header().Set("Cache-Control", "no-store")
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusBadRequest, Detail: "The price is a metered price. A metered price is not bought alone when the plan has a licensed price. Try: select a licensed price of the plan from GET /api/billing/plans.",
			Errors: []httpx.FieldError{{Field: "price", Message: "Use a licensed price lookup key."}}})
		return
	}
	customer, err := s.ensureCustomer(ctx, u.ID)
	if err != nil {
		var se *stripe.Error
		if errors.As(err, &se) {
			s.stripeFailed(w, r, "create the customer", err)
			return
		}
		s.internal(w, r, "store the customer", err)
		return
	}
	user := strconv.FormatInt(u.ID, 10)
	session, err := s.client.CreateCheckoutSession(ctx, stripe.CheckoutParams{
		Customer:            customer,
		Price:               price.ID,
		ClientReferenceID:   user,
		SuccessURL:          s.origin + "/settings/billing?session_id={CHECKOUT_SESSION_ID}",
		CancelURL:           s.origin + "/pricing",
		TrialDays:           s.cfg.TrialDays,
		AutomaticTax:        s.cfg.AutomaticTax,
		AllowPromotionCodes: s.cfg.AllowPromotionCodes,
		Metadata:            map[string]string{"user_id": user},
		Metered:             chosenMetered,
		MeteredPrices:       meteredIDs,
	}, "")
	if err != nil {
		s.stripeFailed(w, r, "create the Checkout session", err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, struct {
		URL string `json:"url"`
	}{session.URL})
}

func (s *Service) portal(w http.ResponseWriter, r *http.Request) {
	if !s.configured(w, r) {
		return
	}
	var in struct{}
	if !decodeBody(w, r, &in, nil, []string{}) {
		return
	}
	ctx := r.Context()
	u, _ := auth.UserFrom(ctx)
	customer, err := s.customerOf(ctx, u.ID)
	if err != nil {
		s.internal(w, r, "read the customer", err)
		return
	}
	if customer == "" {
		problem(w, r, http.StatusConflict, "You have no billing account yet. Try: subscribe to a plan on /pricing first.")
		return
	}
	session, err := s.client.CreatePortalSession(ctx, customer, s.origin+"/settings/billing")
	if err != nil {
		s.stripeFailed(w, r, "create the portal session", err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, struct {
		URL string `json:"url"`
	}{session.URL})
}

func (s *Service) syncRoute(w http.ResponseWriter, r *http.Request) {
	if !s.configured(w, r) {
		return
	}
	var in struct {
		SessionID string `json:"session_id"`
	}
	if !decodeBody(w, r, &in, []string{"session_id"}, []string{"session_id"}) {
		return
	}
	ctx := r.Context()
	notFound := func() {
		problem(w, r, http.StatusNotFound, "The checkout session was not found. Try: open the link from the Stripe Checkout page again.")
	}
	if !strings.HasPrefix(in.SessionID, "cs_") || len(in.SessionID) > 255 || strings.ContainsAny(in.SessionID, "/?#% ") {
		notFound()
		return
	}
	u, _ := auth.UserFrom(ctx)
	customer, err := s.customerOf(ctx, u.ID)
	if err != nil {
		s.internal(w, r, "read the customer", err)
		return
	}
	if customer == "" {
		notFound()
		return
	}
	session, err := s.client.GetCheckoutSession(ctx, in.SessionID)
	if err != nil {
		var se *stripe.Error
		if errors.As(err, &se) && se.Code == "resource_missing" {
			notFound()
			return
		}
		s.stripeFailed(w, r, "read the Checkout session", err)
		return
	}
	if session.ClientReferenceID != strconv.FormatInt(u.ID, 10) || session.Customer != customer {
		notFound()
		return
	}
	if err := s.Sync(ctx, customer); err != nil {
		var se *stripe.Error
		if errors.As(err, &se) {
			s.stripeFailed(w, r, "list the subscriptions", err)
			return
		}
		s.internal(w, r, "sync the customer", err)
		return
	}
	e, err := s.Entitlement(ctx, u.ID)
	if err != nil {
		s.internal(w, r, "read the entitlement", err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, billingView(e))
}

// catalog returns the active Stripe prices of the declared lookup keys. It
// keeps them for catalogTTL. The mutex makes concurrent misses share one
// Stripe call. When Stripe fails, it returns the old prices if it has them.
func (s *Service) catalog(ctx context.Context) (map[string]stripe.Price, error) {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	if s.catalogPrices != nil && s.now().Sub(s.catalogAt) < catalogTTL {
		return s.catalogPrices, nil
	}
	prices, err := s.listPrices(ctx, s.cfg.lookupKeys())
	if err != nil {
		if s.catalogPrices != nil {
			s.logger.WarnContext(ctx, "billing: cannot refresh the Stripe prices, so the plans route uses the old prices", "error", err)
			return s.catalogPrices, nil
		}
		return nil, err
	}
	byKey := make(map[string]stripe.Price, len(prices))
	for _, p := range prices {
		if p.LookupKey != "" {
			byKey[p.LookupKey] = p
		}
	}
	s.catalogPrices, s.catalogAt = byKey, s.now()
	return byKey, nil
}

// listPrices asks for at most 10 lookup keys in each call, the Stripe limit.
func (s *Service) listPrices(ctx context.Context, keys []string) ([]stripe.Price, error) {
	var out []stripe.Price
	for chunk := range slices.Chunk(keys, 10) {
		prices, err := s.client.ListPrices(ctx, chunk)
		if err != nil {
			return nil, err
		}
		out = append(out, prices...)
	}
	return out, nil
}

type priceJSON struct {
	LookupKey     string `json:"lookup_key"`
	Currency      string `json:"currency"`
	UnitAmount    int64  `json:"unit_amount"`
	Interval      string `json:"interval"`
	IntervalCount int    `json:"interval_count"`
	UsageType     string `json:"usage_type"`
	ProductName   string `json:"product_name"`
}

type planJSON struct {
	Name     string      `json:"name"`
	Features []string    `json:"features"`
	Prices   []priceJSON `json:"prices"`
}

func (s *Service) plans(w http.ResponseWriter, r *http.Request) {
	if !s.configured(w, r) {
		return
	}
	ctx := r.Context()
	prices, err := s.catalog(ctx)
	if err != nil {
		s.logger.ErrorContext(ctx, "billing: cannot list the Stripe prices", "error", err, "request_id", httpx.RequestIDFrom(ctx))
		problem(w, r, http.StatusServiceUnavailable, "The plans are not available now. Try: retry in a minute.")
		return
	}
	out := make([]planJSON, 0, len(s.cfg.Plans))
	for _, p := range s.cfg.Plans {
		view := planJSON{Name: p.Name, Features: nonNil(slices.Clone(p.Features)), Prices: []priceJSON{}}
		for _, key := range p.Prices {
			price, ok := prices[key]
			if !ok {
				s.warnOnce(ctx, "price:"+key, "billing: a declared price lookup key is not an active Stripe price, so the plans route leaves it out. Try: run billing check.", "lookup_key", key)
				continue
			}
			pv := priceJSON{LookupKey: key, Currency: price.Currency, UnitAmount: price.UnitAmount, ProductName: price.Product.Name}
			if price.Recurring != nil {
				pv.Interval, pv.IntervalCount, pv.UsageType = price.Recurring.Interval, price.Recurring.IntervalCount, price.Recurring.UsageType
			}
			view.Prices = append(view.Prices, pv)
		}
		out = append(out, view)
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	_ = httpx.WriteJSON(w, http.StatusOK, struct {
		Plans []planJSON `json:"plans"`
	}{out})
}

// configured answers 503 when the service has no Stripe client.
func (s *Service) configured(w http.ResponseWriter, r *http.Request) bool {
	if s.client != nil {
		return true
	}
	problem(w, r, http.StatusServiceUnavailable, "Billing is not configured on this server. Try: set STRIPE_SECRET_KEY and restart the API.")
	return false
}

// isMetered reports whether the price bills reported usage.
func isMetered(p stripe.Price) bool {
	return p.Recurring != nil && p.Recurring.UsageType == "metered"
}
