package auth

import (
	"context"
	"math"
	"mime"
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	"github.com/stopyellingatme/spinup/kit/httpx"
)

// maxBody limits the JSON body of the auth endpoints.
const maxBody = 16 << 10

// maxEmailLength is the longest email address that fits in SMTP (RFC 5321).
const maxEmailLength = 254

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type userResponse struct {
	User User `json:"user"`
}

// Routes registers the auth endpoints (ADR 0008 §3). They need Middleware.
//
//	POST /api/auth/register  {"email","password"} -> 201 {"user"}, 409, 422
//	POST /api/auth/login     {"email","password"} -> 200 {"user"}, 401, 422, 429
//	POST /api/auth/logout                         -> 204
//	GET  /api/session                             -> 200 {"user"}, 401
//
// Errors are problem details. A 422 problem lists field errors for "email" and
// "password". Register and login set the session cookie with a new token.
func (a *Auth) Routes(mux *http.ServeMux) {
	a.oidcRoutes(mux)
	mux.Handle("POST /api/auth/register", a.endpoint(a.register))
	mux.Handle("POST /api/auth/login", a.endpoint(a.login))
	mux.Handle("POST /api/auth/logout", a.endpoint(a.logout))
	mux.Handle("GET /api/session", a.endpoint(a.session))
}

func (a *Auth) endpoint(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.installed(w, r) {
			return
		}
		noStore(w)
		h(w, r)
	})
}

func (a *Auth) register(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeCredentials(w, r)
	if !ok {
		return
	}
	email, emailOK := normalizeEmail(in.Email)
	var fields []httpx.FieldError
	if !emailOK {
		fields = append(fields, httpx.FieldError{Field: "email", Message: "Enter a valid email address."})
	}
	switch n := passwordLength(in.Password); {
	case n < MinPasswordLength:
		fields = append(fields, httpx.FieldError{Field: "password", Message: "Use at least " + strconv.Itoa(MinPasswordLength) + " characters."})
	case n > MaxPasswordLength:
		fields = append(fields, httpx.FieldError{Field: "password", Message: "Use at most " + strconv.Itoa(MaxPasswordLength) + " characters."})
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	hash, err := a.hash(r.Context(), in.Password)
	if err != nil {
		a.internalError(w, r, "hash a password", err)
		return
	}
	user, created, err := a.createUser(r.Context(), email, hash)
	if err != nil {
		a.internalError(w, r, "create a user", err)
		return
	}
	if !created {
		httpx.WriteProblem(w, r, httpx.Problem{
			Status: http.StatusConflict,
			Detail: "An account with this email address already exists.",
			Errors: []httpx.FieldError{{Field: "email", Message: "This email address is already registered."}},
		})
		return
	}
	if err := a.startSession(r.Context(), user); err != nil {
		a.internalError(w, r, "start a session", err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, userResponse{User: user})
}

func (a *Auth) login(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeCredentials(w, r)
	if !ok {
		return
	}
	email, emailOK := normalizeEmail(in.Email)
	var fields []httpx.FieldError
	if !emailOK {
		fields = append(fields, httpx.FieldError{Field: "email", Message: "Enter a valid email address."})
	}
	switch n := passwordLength(in.Password); {
	case n == 0:
		fields = append(fields, httpx.FieldError{Field: "password", Message: "Enter your password."})
	case n > MaxPasswordLength:
		fields = append(fields, httpx.FieldError{Field: "password", Message: "Use at most " + strconv.Itoa(MaxPasswordLength) + " characters."})
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	ctx := r.Context()
	now := a.now()
	until, err := a.reserveAttempt(ctx, email, now)
	if err != nil {
		a.internalError(w, r, "reserve a login attempt", err)
		return
	}
	if !until.IsZero() {
		wait := max(1, int(math.Ceil(until.Sub(now).Seconds())))
		w.Header().Set("Retry-After", strconv.Itoa(wait))
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusTooManyRequests, Detail: "Too many failed logins for this email address. Try again later."})
		return
	}
	user, stored, found, err := a.credentialsByEmail(ctx, email)
	if err != nil {
		a.internalError(w, r, "read a user", err)
		return
	}
	if !found {
		// Spend the same work as a real check, so the time does not reveal accounts.
		stored = a.dummy
	}
	var match bool
	var used params
	var verifyErr error
	if err := a.slots.run(ctx, func() { match, used, verifyErr = verifyPassword(stored, in.Password) }); err != nil {
		verifyErr = err
	}
	if verifyErr != nil {
		a.internalError(w, r, "verify a password", verifyErr)
		return
	}
	if !found || !match {
		noStore(w)
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusUnauthorized, Detail: "The email address or password is incorrect."})
		return
	}
	if err := a.clearFailures(ctx, email); err != nil {
		a.internalError(w, r, "clear failed logins", err)
		return
	}
	if used != a.params {
		a.rehash(ctx, user, stored, in.Password)
	}
	if err := a.startSession(ctx, user); err != nil {
		a.internalError(w, r, "start a session", err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, userResponse{User: user})
}

func (a *Auth) logout(w http.ResponseWriter, r *http.Request) {
	// Destroy deletes the session row and expires the cookie, also for a session
	// that has no user.
	if err := a.sessions.Destroy(r.Context()); err != nil {
		a.internalError(w, r, "destroy a session", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Auth) session(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFrom(r.Context())
	if !ok {
		unauthorized(w, r)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, userResponse{User: user})
}

// startSession signs user in on a new session token. The old token stops working,
// which prevents session fixation.
func (a *Auth) startSession(ctx context.Context, user User) error {
	if err := a.sessions.Clear(ctx); err != nil {
		return err
	}
	if err := a.sessions.RenewToken(ctx); err != nil {
		return err
	}
	a.sessions.Put(ctx, sessionUserKey, user.ID)
	return nil
}

// hash computes the hash of password in a hash slot.
func (a *Auth) hash(ctx context.Context, password string) (string, error) {
	var hash string
	err := a.slots.run(ctx, func() { hash = hashPassword(a.params, password) })
	return hash, err
}

// rehash stores a hash with the current parameters after a successful login with
// older ones. A failure only logs: the old hash still works.
func (a *Auth) rehash(ctx context.Context, user User, old, password string) {
	hash, err := a.hash(ctx, password)
	if err == nil {
		err = a.updatePasswordHash(ctx, user.ID, old, hash)
	}
	if err != nil {
		a.logger.WarnContext(ctx, "auth: cannot rehash a password with the current parameters", "error", err, "request_id", httpx.RequestIDFrom(ctx))
	}
}

// decodeCredentials reads the JSON body. It writes the problem and returns false
// when the body is not one JSON object with known fields.
func decodeCredentials(w http.ResponseWriter, r *http.Request) (credentials, bool) {
	var in credentials
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusUnsupportedMediaType, Detail: "The request body must be application/json."})
		return in, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteProblem(w, r, httpx.ProblemFrom(err))
		return in, false
	}
	return in, true
}

func invalid(w http.ResponseWriter, r *http.Request, fields []httpx.FieldError) {
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusUnprocessableEntity, Detail: "The request has invalid fields.", Errors: fields})
}

// normalizeEmail trims and lower-cases s. ok is false unless the result is a bare
// address (no display name, no angle brackets) that net/mail accepts.
func normalizeEmail(s string) (email string, ok bool) {
	email = strings.ToLower(strings.TrimSpace(s))
	if email == "" || len(email) > maxEmailLength {
		return email, false
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Name != "" || addr.Address != email {
		return email, false
	}
	return email, true
}
