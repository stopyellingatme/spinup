package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/stopyellingatme/spinup/kit/config"
	"github.com/stopyellingatme/spinup/kit/httpx"
)

const oidcLifetime = 10 * time.Minute

var oidcName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type oidcSettings struct {
	Names  string `env:"AUTH_OIDC_PROVIDERS"`
	Origin string `env:"PUBLIC_ORIGIN" default:"http://localhost:5173"`
	Key    string `env:"AUTH_OIDC_COOKIE_KEY"`
}

type oidcProvider struct {
	name, label, issuer, clientID, secret string
	scopes                                []string
	err                                   error
	mu                                    sync.Mutex
	discovered                            *oidc.Provider
}

type oidcHandler struct {
	auth      *Auth
	origin    string
	providers []*oidcProvider
	cipher    cipher.AEAD
	err       error
	client    *http.Client
	user      func(context.Context, string, string, string, bool) (User, error)
}

type oidcFlow struct {
	Provider string `json:"provider"`
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	Next     string `json:"next"`
	Expires  int64  `json:"expires"`
}

// oidcRoutes reads configuration without contacting providers. Discovery errors
// affect only the provider route, and never prevent the application from starting.
func (a *Auth) oidcRoutes(mux *http.ServeMux) {
	h := &oidcHandler{auth: a, client: &http.Client{Timeout: 15 * time.Second}, user: a.oidcUser}
	var settings oidcSettings
	h.err = config.Load(&settings)
	if strings.TrimSpace(settings.Names) == "" && h.err == nil {
		return
	}
	h.origin = settings.Origin
	if !oidcOriginOK(h.origin, a.sessions.Cookie.Secure) {
		h.err = errors.New("invalid PUBLIC_ORIGIN")
	}
	key := make([]byte, 32)
	if settings.Key == "" {
		_, _ = rand.Read(key)
	} else {
		var err error
		key, err = base64.RawURLEncoding.Strict().DecodeString(settings.Key)
		if err != nil || len(key) != 32 {
			h.err = errors.New("AUTH_OIDC_COOKIE_KEY must encode 32 bytes as unpadded base64url")
			key = make([]byte, 32)
		}
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err) // key is always 32 bytes.
	}
	h.cipher, err = cipher.NewGCM(block)
	if err != nil {
		panic(err) // AES has the required block size.
	}
	seen := map[string]bool{}
	for _, name := range strings.Split(settings.Names, ",") {
		name = strings.TrimSpace(name)
		if !oidcName.MatchString(name) || seen[name] {
			h.err = errors.New("AUTH_OIDC_PROVIDERS must list distinct lower-case provider names")
			continue
		}
		seen[name] = true
		h.providers = append(h.providers, loadOIDCProvider(name))
	}
	mux.Handle("GET /api/auth/oidc/providers", a.endpoint(h.list))
	mux.Handle("GET /api/auth/oidc/{provider}/start", a.endpoint(h.start))
	mux.Handle("GET /api/auth/oidc/{provider}/callback", a.endpoint(h.callback))
}

func loadOIDCProvider(name string) *oidcProvider {
	var c struct {
		Issuer string `env:"ISSUER" required:"true"`
		ID     string `env:"CLIENT_ID" required:"true"`
		Secret string `env:"CLIENT_SECRET"`
		Scopes string `env:"SCOPES"`
		Label  string `env:"LABEL"`
	}
	prefix := "AUTH_OIDC_" + strings.ToUpper(name) + "_"
	err := config.LoadFrom(&c, func(key string) (string, bool) { return os.LookupEnv(prefix + key) }, os.ReadFile)
	if err == nil && !oidcOriginIssuerOK(c.Issuer) {
		err = fmt.Errorf("%sISSUER is invalid", prefix)
	}
	label := c.Label
	if label == "" {
		label = name
	}
	scopes := []string{oidc.ScopeOpenID, "email", "profile"}
	for _, scope := range strings.Fields(strings.ReplaceAll(c.Scopes, ",", " ")) {
		found := false
		for _, s := range scopes {
			found = found || s == scope
		}
		if !found {
			scopes = append(scopes, scope)
		}
	}
	return &oidcProvider{name: name, label: label, issuer: c.Issuer, clientID: c.ID, secret: c.Secret, scopes: scopes, err: err}
}

func oidcOriginIssuerOK(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "https" || u.Scheme == "http")
}

func oidcOriginOK(raw string, secure bool) bool {
	if !oidcOriginIssuerOK(raw) {
		return false
	}
	u, _ := url.Parse(raw)
	return u.Path == "" && (!secure || u.Scheme == "https")
}

func (h *oidcHandler) list(w http.ResponseWriter, _ *http.Request) {
	type provider struct {
		Name  string `json:"name"`
		Label string `json:"label"`
	}
	out := make([]provider, 0, len(h.providers))
	for _, p := range h.providers {
		out = append(out, provider{p.name, p.label})
	}
	_ = httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *oidcHandler) provider(w http.ResponseWriter, r *http.Request) *oidcProvider {
	if h.err != nil {
		oidcUnavailable(w, r)
		return nil
	}
	for _, p := range h.providers {
		if p.name == r.PathValue("provider") {
			if p.err != nil {
				oidcUnavailable(w, r)
				return nil
			}
			return p
		}
	}
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusNotFound, Detail: "The sign-in provider is unknown.\nTry: choose a configured provider."})
	return nil
}

func (h *oidcHandler) discover(ctx context.Context, p *oidcProvider) (*oidc.Provider, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.discovered == nil {
		var err error
		p.discovered, err = oidc.NewProvider(oidc.ClientContext(ctx, h.client), p.issuer)
		if err != nil {
			return nil, err
		}
	}
	return p.discovered, nil
}

func (h *oidcHandler) oauth(p *oidcProvider, discovered *oidc.Provider) oauth2.Config {
	return oauth2.Config{ClientID: p.clientID, ClientSecret: p.secret, Endpoint: discovered.Endpoint(),
		Scopes: p.scopes, RedirectURL: h.origin + "/api/auth/oidc/" + p.name + "/callback"}
}

func (h *oidcHandler) start(w http.ResponseWriter, r *http.Request) {
	p := h.provider(w, r)
	if p == nil {
		return
	}
	next := r.URL.Query().Get("next")
	if next == "" {
		next = "/account"
	}
	if !oidcRedirectOK(next) || len(next) > 1024 {
		oidcInvalid(w, r)
		return
	}
	discovered, err := h.discover(r.Context(), p)
	if err != nil {
		oidcUnavailable(w, r)
		return
	}
	flow := oidcFlow{Provider: p.name, State: oauth2.GenerateVerifier(), Nonce: oauth2.GenerateVerifier(),
		Verifier: oauth2.GenerateVerifier(), Next: next, Expires: h.auth.now().Add(oidcLifetime).Unix()}
	data, err := json.Marshal(flow)
	if err != nil {
		h.auth.internalError(w, r, "encode the OIDC flow", err)
		return
	}
	nonce := make([]byte, h.cipher.NonceSize())
	_, _ = rand.Read(nonce)
	sealed := h.cipher.Seal(nonce, nonce, data, []byte(p.name))
	cookie := h.cookie(p.name)
	cookie.Value = base64.RawURLEncoding.EncodeToString(sealed)
	cookie.MaxAge = int(oidcLifetime.Seconds())
	cookie.Expires = h.auth.now().Add(oidcLifetime)
	http.SetCookie(w, cookie)
	cfg := h.oauth(p, discovered)
	http.Redirect(w, r, cfg.AuthCodeURL(flow.State, oidc.Nonce(flow.Nonce), oauth2.S256ChallengeOption(flow.Verifier)), http.StatusSeeOther)
}

func (h *oidcHandler) cookie(provider string) *http.Cookie {
	return &http.Cookie{Name: "oidc_" + provider, Path: "/api/auth/oidc/" + provider + "/", HttpOnly: true,
		Secure: h.auth.sessions.Cookie.Secure, SameSite: http.SameSiteLaxMode}
}

func (h *oidcHandler) flow(r *http.Request, p *oidcProvider) (oidcFlow, error) {
	var flow oidcFlow
	cookie, err := r.Cookie(h.cookie(p.name).Name)
	if err != nil || len(cookie.Value) > 4096 {
		return flow, errors.New("missing flow cookie")
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(cookie.Value)
	n := h.cipher.NonceSize()
	if err != nil || len(data) < n {
		return flow, errors.New("invalid flow cookie")
	}
	plain, err := h.cipher.Open(nil, data[:n], data[n:], []byte(p.name))
	if err != nil {
		return flow, err
	}
	if err := json.Unmarshal(plain, &flow); err != nil {
		return flow, err
	}
	if flow.Provider != p.name || flow.State == "" || flow.Nonce == "" || flow.Verifier == "" ||
		flow.Expires <= h.auth.now().Unix() || flow.Expires > h.auth.now().Add(oidcLifetime).Unix() || !oidcRedirectOK(flow.Next) {
		return flow, errors.New("expired or invalid flow")
	}
	return flow, nil
}

func (h *oidcHandler) callback(w http.ResponseWriter, r *http.Request) {
	p := h.provider(w, r)
	if p == nil {
		return
	}
	cookie := h.cookie(p.name)
	cookie.MaxAge = -1
	http.SetCookie(w, cookie)
	flow, err := h.flow(r, p)
	query := r.URL.Query()
	if err != nil || len(query["state"]) != 1 || subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(flow.State)) != 1 ||
		query.Get("error") != "" || len(query["code"]) != 1 || query.Get("code") == "" {
		oidcInvalid(w, r)
		return
	}
	discovered, err := h.discover(r.Context(), p)
	if err != nil {
		oidcUnavailable(w, r)
		return
	}
	ctx := oidc.ClientContext(r.Context(), h.client)
	cfg := h.oauth(p, discovered)
	token, err := cfg.Exchange(ctx, query.Get("code"), oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		oidcInvalid(w, r)
		return
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		oidcInvalid(w, r)
		return
	}
	id, err := discovered.Verifier(&oidc.Config{ClientID: p.clientID}).Verify(ctx, raw)
	if err != nil || subtle.ConstantTimeCompare([]byte(id.Nonce), []byte(flow.Nonce)) != 1 || id.Subject == "" {
		oidcInvalid(w, r)
		return
	}
	var claims struct {
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
	}
	if err := id.Claims(&claims); err != nil {
		oidcInvalid(w, r)
		return
	}
	email, valid := normalizeEmail(claims.Email)
	if !valid || !claims.Verified {
		oidcInvalid(w, r)
		return
	}
	user, err := h.user(ctx, p.name, id.Subject, email, claims.Verified)
	if err != nil {
		h.auth.internalError(w, r, "link the OIDC identity", err)
		return
	}
	if err := h.auth.startSession(ctx, user); err != nil {
		h.auth.internalError(w, r, "start the OIDC session", err)
		return
	}
	http.Redirect(w, r, flow.Next, http.StatusSeeOther)
}

func oidcRedirectOK(path string) bool {
	u, err := url.Parse(path)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Opaque != "" {
		return false
	}
	// Check decoded paths too. Browsers treat backslashes as separators.
	for _, v := range []string{path, u.Path} {
		if !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.ContainsAny(v, "\\\r\n\x00\t") {
			return false
		}
	}
	return true
}

func oidcInvalid(w http.ResponseWriter, r *http.Request) {
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusBadRequest,
		Detail: "The sign-in response is invalid, expired, or has no verified email.\nTry: start sign-in again with a verified email."})
}

func oidcUnavailable(w http.ResponseWriter, r *http.Request) {
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusServiceUnavailable,
		Detail: "This sign-in provider is unavailable or its configuration is invalid.\nTry: check AUTH_OIDC settings and provider availability, then retry."})
}
