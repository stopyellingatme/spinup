package dofake

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type authCode struct {
	challenge, redirect, scope string
	used                       bool
}

// grant is one pair of access and refresh tokens.
type grant struct {
	access, refresh, scope string
	expires                time.Time
}

func oauthError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func (s *Server) oauth(w http.ResponseWriter, r *http.Request, body []byte) {
	switch {
	case r.URL.Path == "/v1/oauth/authorize" && r.Method == http.MethodGet:
		s.authorize(w, r)
	case r.URL.Path == "/v1/oauth/token" && r.Method == http.MethodPost:
		s.token(w, r)
	case r.URL.Path == "/v1/oauth/revoke" && r.Method == http.MethodPost:
		s.revoke(w, r)
	case r.URL.Path == "/v1/oauth/register" && r.Method == http.MethodPost:
		s.register(w, body)
	default:
		notFound(w)
	}
}

// validRedirect accepts http://127.0.0.1|localhost|[::1]:<any port>/callback.
func validRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Path != "/callback" {
		return false
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect := q.Get("redirect_uri")
	switch {
	case q.Get("client_id") != s.o.ClientID:
		http.Error(w, "unknown client_id", http.StatusBadRequest)
		return
	case !validRedirect(redirect):
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	back, _ := url.Parse(redirect)
	rq := back.Query()
	if q.Get("state") != "" {
		rq.Set("state", q.Get("state"))
	}
	switch {
	case s.o.Deny:
		rq.Set("error", "access_denied")
	case q.Get("response_type") != "code":
		rq.Set("error", "unsupported_response_type")
	case q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "":
		rq.Set("error", "invalid_request")
		rq.Set("error_description", "PKCE with S256 is required")
	default:
		code := randHex(16)
		s.codes[code] = &authCode{challenge: q.Get("code_challenge"), redirect: redirect, scope: q.Get("scope")}
		rq.Set("code", code)
	}
	back.RawQuery = rq.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		oauthError(w, 400, "invalid_request")
		return
	}
	f := r.PostForm
	if f.Get("client_id") != s.o.ClientID {
		oauthError(w, 401, "invalid_client")
		return
	}
	switch f.Get("grant_type") {
	case "authorization_code":
		c := s.codes[f.Get("code")]
		if c == nil || c.used {
			oauthError(w, 400, "invalid_grant")
			return
		}
		c.used = true
		sum := sha256.Sum256([]byte(f.Get("code_verifier")))
		want := base64.RawURLEncoding.EncodeToString(sum[:])
		if f.Get("redirect_uri") != c.redirect || subtle.ConstantTimeCompare([]byte(want), []byte(c.challenge)) != 1 {
			oauthError(w, 400, "invalid_grant")
			return
		}
		s.issue(w, c.scope)
	case "refresh_token":
		g := s.refresh[f.Get("refresh_token")]
		if g == nil {
			oauthError(w, 400, "invalid_grant")
			return
		}
		s.drop(g)
		s.issue(w, g.scope)
	default:
		oauthError(w, 400, "unsupported_grant_type")
	}
}

// drop removes both tokens of a grant.
func (s *Server) drop(g *grant) {
	delete(s.access, g.access)
	delete(s.refresh, g.refresh)
}

func (s *Server) issue(w http.ResponseWriter, scope string) {
	g := &grant{access: "dop_v1_" + randHex(24), refresh: "dor_v1_" + randHex(24), scope: scope,
		expires: s.o.Now().Add(s.o.AccessTTL)}
	s.access[g.access], s.refresh[g.refresh] = g, g
	s.issued++
	writeJSON(w, 200, map[string]any{
		"access_token":  g.access,
		"token_type":    "bearer",
		"expires_in":    int(s.o.AccessTTL.Seconds()),
		"refresh_token": g.refresh,
		"scope":         scope,
		"info": map[string]string{
			"name": "Dev", "email": s.o.Email, "uuid": "fake-user-uuid",
			"team_uuid": s.o.TeamUUID, "team_name": s.o.TeamName,
		},
	})
}

// revoke accepts the access or the refresh token. It drops both tokens of the
// grant. It answers 200 for an unknown token (RFC 7009). It does not check the
// Bearer header, so a client can revoke the second token after the first.
func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	tok := r.Form.Get("token")
	if g := s.access[tok]; g != nil {
		s.drop(g)
	} else if g := s.refresh[tok]; g != nil {
		s.drop(g)
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) register(w http.ResponseWriter, body []byte) {
	var req map[string]any
	if json.Unmarshal(body, &req) != nil || req["token_endpoint_auth_method"] != "none" {
		oauthError(w, 400, "invalid_client_metadata")
		return
	}
	req["client_id"] = s.o.ClientID
	req["registration_access_token"] = "reg-" + randHex(16)
	writeJSON(w, 201, req)
}

// bearerOK reports whether the request holds a valid API token.
func (s *Server) bearerOK(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return false
	}
	for _, t := range s.o.StaticTokens {
		if t == tok {
			return true
		}
	}
	g := s.access[tok]
	return g != nil && s.o.Now().Before(g.expires)
}
