package cloudauth

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// LoginOptions controls the browser login.
type LoginOptions struct {
	// Open opens the URL in a browser. Nil means do not open. An Open error does not stop the login,
	// because the URL is always printed.
	Open func(url string) error
	// Out receives the URL. Nil discards it.
	Out io.Writer
	// Timeout ends the wait for the browser. The default is 5 minutes.
	Timeout time.Duration
	// Addr is the listen address. The default is 127.0.0.1:0.
	Addr string
	// CallbackPath is the redirect path. The default is /callback.
	CallbackPath string
	// TryHint is the Try line of the timeout error. The default names --no-browser.
	TryHint string
}

type callback struct {
	code string
	err  error
}

const pageCSP = "default-src 'none'; style-src 'unsafe-inline'"

func page(w http.ResponseWriter, status int, title, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", pageCSP)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "close")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>%s</title>"+
		"<body style=\"font-family:sans-serif;margin:3em\"><h1>%s</h1><p>%s</p></body>", title, title, msg)
}

// Login runs the authorization code flow with PKCE and returns the token.
//
// A request to the callback path with no state is answered with 400 and ignored.
// A request to another path is answered with 404 and ignored.
// A callback with a state that does not match fails the login, because it can be a forged request.
func Login(ctx context.Context, cfg Config, o LoginOptions) (Token, error) {
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Minute
	}
	if o.Addr == "" {
		o.Addr = "127.0.0.1:0"
	}
	if o.CallbackPath == "" {
		o.CallbackPath = "/callback"
	}
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	verifier, err := NewVerifier()
	if err != nil {
		return Token{}, err
	}
	state, err := NewState()
	if err != nil {
		return Token{}, err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", o.Addr)
	if err != nil {
		return Token{}, problem("start the sign-in listener", err.Error(), "free the address "+o.Addr+", or use a personal access token")
	}
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	redirect := "http://" + net.JoinHostPort(host, port) + o.CallbackPath

	results := make(chan callback, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(o.CallbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		got := q.Get("state")
		if got == "" {
			page(w, http.StatusBadRequest, "Sign-in failed", "The request has no state value. You can close this tab.")
			return
		}
		var res callback
		switch {
		case subtle.ConstantTimeCompare([]byte(got), []byte(state)) != 1:
			res.err = problem("sign in", "the state value of the callback does not match", "run the sign-in again")
		case q.Get("error") != "":
			res.err = callbackError(q.Get("error"), q.Get("error_description"))
		case q.Get("code") == "":
			res.err = problem("sign in", "the callback has no code", "run the sign-in again")
		default:
			res.code = q.Get("code")
		}
		select {
		case results <- res:
		default:
			page(w, http.StatusConflict, "Sign-in already handled", "You can close this tab.")
			return
		}
		if res.err != nil {
			page(w, http.StatusBadRequest, "Sign-in failed", "Return to the terminal for details. You can close this tab.")
			return
		}
		page(w, http.StatusOK, "Signed in", "Return to the terminal. You can close this tab.")
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	authURL := buildAuthorizeURL(cfg, redirect, state, Challenge(verifier))
	_, _ = fmt.Fprintf(out, "Open this URL in a browser to sign in:\n%s\nIf the browser did not open, open the URL yourself.\n", authURL)
	if o.Open != nil {
		_ = o.Open(authURL)
	}

	timer := time.NewTimer(o.Timeout)
	defer timer.Stop()
	select {
	case res := <-results:
		if res.err != nil {
			return Token{}, res.err
		}
		return Exchange(ctx, cfg, res.code, verifier, redirect)
	case <-timer.C:
		try := "run the command again with --no-browser"
		if o.TryHint != "" {
			try = o.TryHint
		}
		return Token{}, problem("sign in", fmt.Sprintf("no answer came from the browser in %s", o.Timeout), try)
	case <-ctx.Done():
		return Token{}, fmt.Errorf("sign in stopped: %w", ctx.Err())
	}
}

func callbackError(code, desc string) error {
	if code == "access_denied" {
		return problem("sign in", "the sign-in was cancelled in the browser", "run the sign-in again")
	}
	why := code
	if desc != "" {
		why += " (" + desc + ")"
	}
	return problem("sign in", "the server refused the sign-in: "+why, "run the sign-in again")
}

func buildAuthorizeURL(cfg Config, redirect, state, challenge string) string {
	v := url.Values{
		"response_type":         {"code"},
		"client_id":             {cfg.ClientID},
		"redirect_uri":          {redirect},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if len(cfg.Scopes) > 0 {
		v.Set("scope", strings.Join(cfg.Scopes, " "))
	}
	sep := "?"
	if strings.Contains(cfg.AuthorizeURL, "?") {
		sep = "&"
	}
	return cfg.AuthorizeURL + sep + v.Encode()
}
