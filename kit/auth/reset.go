package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	netmail "net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/stopyellingatme/spinup/kit/httpx"
	"github.com/stopyellingatme/spinup/kit/mail"
)

// ResetLifetime is the validity period of a password reset token.
const ResetLifetime = time.Hour

// ResetOption configures the optional password reset routes.
type ResetOption func(*resetOptions)

type resetOptions struct {
	mailer        mail.Mailer
	from, baseURL string
	templates     fs.FS
}

// WithMailer enables password resets with m. A nil mailer leaves the routes absent.
func WithMailer(m mail.Mailer) ResetOption { return func(o *resetOptions) { o.mailer = m } }

// WithResetURL sets the trusted public URL before the token, such as https://app.example/reset.
// The URL never comes from a request header.
func WithResetURL(baseURL string) ResetOption { return func(o *resetOptions) { o.baseURL = baseURL } }

// WithResetFrom sets the sender address for password reset emails.
func WithResetFrom(from string) ResetOption { return func(o *resetOptions) { o.from = from } }

// WithResetTemplates uses embedded password_reset.{subject,txt,html}.tmpl files.
// Each template receives ResetData. Without this option, the kit uses plain text.
func WithResetTemplates(files fs.FS) ResetOption {
	return func(o *resetOptions) { o.templates = files }
}

// ResetData is the data available to password reset templates. It never holds a password.
type ResetData struct {
	// URL is the trusted public reset link, including the one-use token.
	URL string
}

type resetHandler struct {
	auth     *Auth
	opts     resetOptions
	requests chan string
}

// ResetRoutes registers password reset endpoints only when WithMailer configures a mailer.
// Call it once before serving, and install Auth.Middleware on the mux.
// Requests enter a bounded background queue, so account lookup and delivery cannot reveal accounts through response times.
func (a *Auth) ResetRoutes(mux *http.ServeMux, opts ...ResetOption) error {
	var o resetOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.mailer == nil {
		return nil
	}
	u, err := url.Parse(o.baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("auth: cannot enable password reset: the public reset URL is invalid\nTry: pass the public /reset URL to auth.WithResetURL")
	}
	o.baseURL = strings.TrimRight(o.baseURL, "/")
	if _, err := netmail.ParseAddress(o.from); err != nil {
		return errors.New("auth: cannot enable password reset: the sender is invalid\nTry: set MAIL_FROM to a valid sender address")
	}
	if _, err := resetMessage(o, "check@example.com", "token"); err != nil {
		return fmt.Errorf("auth: cannot enable password reset: %w\nTry: check MAIL_FROM and the reset templates", err)
	}
	h := &resetHandler{auth: a, opts: o, requests: make(chan string, 64)}
	mux.Handle("POST /api/auth/reset/request", a.endpoint(h.request))
	mux.Handle("POST /api/auth/reset/confirm", a.endpoint(h.confirm))
	go h.deliverLoop()
	return nil
}

func (h *resetHandler) request(w http.ResponseWriter, r *http.Request) {
	// Even malformed and throttled requests receive the same empty response.
	// Do not read account data in this handler.
	var in struct {
		Email string `json:"email"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := httpx.DecodeJSON(r, &in); err == nil {
		if email, ok := normalizeEmail(in.Email); ok {
			ip, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				ip = r.RemoteAddr
			}
			// Forwarded headers are untrusted. Use the direct peer address.
			now := h.auth.now()
			ipUntil, ipErr := h.auth.reserveAttempt(r.Context(), "reset-ip:"+ip, now)
			emailUntil, emailErr := h.auth.reserveAttempt(r.Context(), "reset-email:"+email, now)
			if ipErr == nil && emailErr == nil && ipUntil.IsZero() && emailUntil.IsZero() {
				select {
				case h.requests <- email:
				default:
				}
			}
		}
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *resetHandler) deliverLoop() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-h.auth.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case email := <-h.requests:
			run, stop := context.WithTimeout(ctx, 30*time.Second)
			if err := h.deliver(run, email); err != nil && ctx.Err() == nil {
				// Never log the email, reset token, or SMTP error content.
				h.auth.logger.ErrorContext(run, "auth: password reset delivery failed", "try", "check the database and mail configuration")
			}
			stop()
		}
	}
}

func (h *resetHandler) deliver(ctx context.Context, email string) error {
	var id int64
	err := h.auth.pool.QueryRow(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&id)
	if errors.Is(err, errResetNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var raw [32]byte
	_, _ = rand.Read(raw[:])
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	digest := sha256.Sum256(raw[:])
	if _, err := h.auth.pool.Exec(ctx, `INSERT INTO password_resets (token_hash,user_id,expires_at) VALUES ($1,$2,$3)`, digest[:], id, h.auth.now().Add(ResetLifetime)); err != nil {
		return err
	}
	msg, err := resetMessage(h.opts, email, token)
	if err != nil {
		return err
	}
	return h.opts.mailer.Send(ctx, msg)
}

func resetMessage(o resetOptions, to, token string) (mail.Message, error) {
	link := o.baseURL + "/" + token
	if o.templates != nil {
		return mail.Render(o.templates, "password_reset", o.from, to, ResetData{URL: link})
	}
	return mail.Message{From: o.from, To: to, Subject: "Reset your password", Text: "Reset your password: " + link + "\nThis link expires in one hour."}, nil
}

func (h *resetHandler) confirm(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := httpx.DecodeJSON(r, &in); err != nil {
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusBadRequest, Detail: "Enter a reset token and a new password."})
		return
	}
	n := passwordLength(in.Password)
	if n < MinPasswordLength || n > MaxPasswordLength {
		invalid(w, r, []httpx.FieldError{{Field: "password", Message: fmt.Sprintf("Use between %d and %d characters.", MinPasswordLength, MaxPasswordLength)}})
		return
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(in.Token)
	if err != nil || len(raw) != 32 {
		resetInvalid(w, r)
		return
	}
	digest := sha256.Sum256(raw)
	// Bound password hashing with the same slots and parameters as registration.
	hash, err := h.auth.hash(r.Context(), in.Password)
	if err != nil {
		h.auth.internalError(w, r, "hash the reset password", err)
		return
	}
	id, err := h.auth.consumeReset(r.Context(), digest[:], hash)
	if errors.Is(err, errResetNotFound) {
		resetInvalid(w, r)
		return
	}
	if err != nil {
		h.auth.internalError(w, r, "confirm the password reset", err)
		return
	}
	if current, ok := h.auth.sessions.Get(r.Context(), sessionUserKey).(int64); ok && current == id {
		if err := h.auth.sessions.Destroy(r.Context()); err != nil {
			h.auth.internalError(w, r, "end the reset session", err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func resetInvalid(w http.ResponseWriter, r *http.Request) {
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusUnprocessableEntity, Detail: "The reset link is invalid or expired. Request a new link."})
}
