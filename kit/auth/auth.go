package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexedwards/scs/pgxstore"
	"github.com/alexedwards/scs/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/httpx"
)

// CookieName is the name of the session cookie (ADR 0008 §3).
const CookieName = "session"

// Default settings. Options change them.
const (
	// DefaultLifetime is the absolute lifetime of a session.
	DefaultLifetime = 7 * 24 * time.Hour
	// DefaultLockoutThreshold is the number of failed logins that locks an email.
	DefaultLockoutThreshold = 5
	// DefaultLockoutWindow is the period in which failed logins count.
	DefaultLockoutWindow = 15 * time.Minute
	// DefaultCSP is the Content-Security-Policy for API responses that set none.
	DefaultCSP = "default-src 'none'; frame-ancestors 'none'"
	// DefaultHashQueue is the number of password checks that can wait for each
	// hash slot. A login that finds the queue full gets 503 with Retry-After.
	DefaultHashQueue = 4
	// DefaultHashWait is the longest time a password check waits for a hash slot.
	DefaultHashWait = 2 * time.Second
)

// cleanupInterval is the time between two deletions of expired rows.
const cleanupInterval = 5 * time.Minute

// User is the account that owns a session.
type User struct {
	// ID is the primary key of the users row.
	ID int64 `json:"id"`
	// Email is the normalized (trimmed, lower-case) email address.
	Email string `json:"email"`
}

// Option configures an Auth.
type Option func(*options)

type options struct {
	secure      bool
	lifetime    time.Duration
	idleTimeout time.Duration
	threshold   int
	window      time.Duration
	logger      *slog.Logger
	origins     []string
	params      params
	cleanup     time.Duration
	now         func() time.Time

	hashConcurrent int
	hashWaiting    int
	hashWait       time.Duration

	rates      RateLimits
	proxies    []string
	proxiesSet bool
	lookup     func(string) (string, bool) // the environment; tests replace it
	sweep      time.Duration
}

// WithSecureCookie sets the Secure attribute of the session cookie. The default is
// true. Turn it off only for local development over plain HTTP.
func WithSecureCookie(secure bool) Option { return func(o *options) { o.secure = secure } }

// WithLifetime sets the absolute lifetime of a session. The default is
// [DefaultLifetime]. A login starts a new lifetime.
func WithLifetime(d time.Duration) Option { return func(o *options) { o.lifetime = d } }

// WithIdleTimeout ends a session after d without a request that carries its cookie.
// The default 0 turns the idle timeout off. With an idle timeout, every request
// with a session writes the session row again and sends a new cookie expiry.
// Server-side requests (for example SvelteKit hooks) do not pass that cookie on
// to the browser, so the browser cookie can expire before the server session.
func WithIdleTimeout(d time.Duration) Option { return func(o *options) { o.idleTimeout = d } }

// WithLockout sets the number of failed logins for one email within window that
// locks logins for that email. The defaults are [DefaultLockoutThreshold] and
// [DefaultLockoutWindow].
func WithLockout(threshold int, window time.Duration) Option {
	return func(o *options) { o.threshold, o.window = threshold, window }
}

// WithHashLimits sets how many password hashes run at the same time
// (concurrent), how many more requests can wait for a slot (waiting), and how
// long a request waits (wait). A request that finds the queue full or waits
// too long gets 503 with Retry-After. The defaults are max(1, GOMAXPROCS/10),
// [DefaultHashQueue] for each slot, and [DefaultHashWait]. Each running hash
// uses 64 MiB of memory.
func WithHashLimits(concurrent, waiting int, wait time.Duration) Option {
	return func(o *options) { o.hashConcurrent, o.hashWaiting, o.hashWait = concurrent, waiting, wait }
}

// WithLogger sets the logger for store failures. A nil logger uses slog.Default.
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.logger = l } }

// WithTrustedOrigins allows unsafe requests from these origins
// ("scheme://host[:port]"), in addition to same-origin requests. Pass the public
// origin of the web app: SvelteKit form actions send it as the Origin header when
// they call the API on its internal address. Empty strings are ignored.
func WithTrustedOrigins(origins ...string) Option {
	return func(o *options) { o.origins = append(o.origins, origins...) }
}

// Auth registers and authenticates users and owns their sessions.
// Construct it with New. It is safe for concurrent use.
type Auth struct {
	pool      *pgxpool.Pool
	sessions  *scs.SessionManager
	csrf      *http.CrossOriginProtection
	logger    *slog.Logger
	params    params
	dummy     string
	slots     *hashSlots
	threshold int
	window    time.Duration
	now       func() time.Time
	limits    *limiter // nil: no rate limit
	proxies   proxySet
	warnedAt  atomic.Int64

	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// New returns an Auth on pool. The pool must hold the users, sessions, and
// login_attempts tables of the auth pack migration. New starts a goroutine that
// deletes expired sessions and old login attempts. Close stops it.
func New(pool *pgxpool.Pool, opts ...Option) (*Auth, error) {
	if pool == nil {
		return nil, errors.New("auth: cannot start: the database pool is nil\nTry: pass the pool from db.Open to auth.New")
	}
	// A hash runs 4 lanes, so each slot keeps 4 goroutines busy. The load test
	// kept other requests fast only when argon2 took at most about 40% of
	// GOMAXPROCS: with more, the goroutines that hold pool connections wait for
	// CPU, and every request that needs the database queues behind them.
	slots := max(1, runtime.GOMAXPROCS(0)/10)
	o := options{secure: true, lifetime: DefaultLifetime, threshold: DefaultLockoutThreshold, window: DefaultLockoutWindow, params: defaultParams, cleanup: cleanupInterval, now: time.Now,
		hashConcurrent: slots, hashWaiting: DefaultHashQueue * slots, hashWait: DefaultHashWait,
		lookup: os.LookupEnv, sweep: logSweepInterval}
	for _, opt := range opts {
		opt(&o)
	}
	settings, err := loadRateSettings(o.lookup)
	if err != nil {
		return nil, err
	}
	switch {
	case o.lifetime <= 0:
		return nil, fmt.Errorf("auth: cannot start: the session lifetime %s is not positive\nTry: pass a positive duration to auth.WithLifetime", o.lifetime)
	case o.idleTimeout < 0:
		return nil, fmt.Errorf("auth: cannot start: the idle timeout %s is negative\nTry: pass 0 (off) or a positive duration to auth.WithIdleTimeout", o.idleTimeout)
	case o.hashConcurrent < 1 || o.hashWaiting < 0 || o.hashWait <= 0:
		return nil, fmt.Errorf("auth: cannot start: the hash limits need at least 1 slot, 0 or more waiting requests, and a positive wait, got %d, %d, and %s\nTry: correct the values of auth.WithHashLimits", o.hashConcurrent, o.hashWaiting, o.hashWait)
	case o.threshold < 1 || o.window <= 0:
		return nil, fmt.Errorf("auth: cannot start: the lockout needs a threshold of at least 1 and a positive window, got %d and %s\nTry: correct the values of auth.WithLockout", o.threshold, o.window)
	}
	rates, clients, err := resolveRates(o.rates, settings)
	if err != nil {
		return nil, err
	}
	trusted := o.proxies
	if !o.proxiesSet {
		trusted = []string{"private"}
		if settings.Proxies != "" {
			trusted = strings.Split(settings.Proxies, ",")
		}
	}
	proxies, err := parseProxySet(trusted)
	if err != nil {
		return nil, err
	}
	if o.logger == nil {
		o.logger = slog.Default()
	}
	a := &Auth{
		pool:      pool,
		logger:    o.logger,
		params:    o.params,
		slots:     newHashSlots(o.hashConcurrent, o.hashWaiting, o.hashWait),
		threshold: o.threshold,
		window:    o.window,
		now:       o.now,
		proxies:   proxies,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}

	a.csrf = http.NewCrossOriginProtection()
	for _, origin := range o.origins {
		if origin == "" {
			continue
		}
		if err := a.csrf.AddTrustedOrigin(origin); err != nil {
			return nil, fmt.Errorf("auth: cannot trust origin %q: %w\nTry: pass an origin of the form scheme://host[:port] with no path", origin, err)
		}
	}
	a.csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusForbidden, Detail: "Cross-origin requests that change state are not allowed."})
	}))

	a.sessions = scs.New()
	// The cleanup goroutine of Auth deletes expired rows, with logging and a clean stop.
	a.sessions.Store = pgxstore.NewWithConfig(pool, pgxstore.Config{CleanUpInterval: 0})
	a.sessions.Lifetime = o.lifetime
	a.sessions.IdleTimeout = o.idleTimeout
	a.sessions.HashTokenInStore = true
	a.sessions.Cookie = scs.SessionCookie{Name: CookieName, HttpOnly: true, Path: "/", SameSite: http.SameSiteLaxMode, Secure: o.secure, Persist: true}
	a.sessions.ErrorFunc = a.sessionError

	// A random password that nobody knows. Logins for unknown emails verify it.
	var secret [32]byte
	_, _ = rand.Read(secret[:])
	a.dummy = hashPassword(a.params, hex.EncodeToString(secret[:]))

	allOff := true
	for _, l := range rates {
		allOff = allOff && l.Off
	}
	if !allOff {
		a.limits = newLimiter(rates, clients, runtime.GOMAXPROCS(0))
	}

	go a.cleanupLoop(o.cleanup, o.sweep)
	return a, nil
}

// Close stops the cleanup goroutine. It does not close the pool. Close has the
// signature of an app.WithShutdown hook, and a second call does nothing.
func (a *Auth) Close(ctx context.Context) error {
	a.closeOnce.Do(func() { close(a.stop) })
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("auth: the cleanup goroutine did not stop: %w\nTry: allow a longer shutdown timeout", ctx.Err())
	}
}

// cleanupLoop deletes expired rows every interval, and sweeps the rate limit table
// and logs the refusals every sweep. A zero interval or no rate limit turns that part off.
func (a *Auth) cleanupLoop(interval, sweep time.Duration) {
	defer close(a.done)
	var dbTick, sweepTick <-chan time.Time
	if interval > 0 {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		dbTick = ticker.C
	}
	if a.limits != nil && sweep > 0 {
		ticker := time.NewTicker(sweep)
		defer ticker.Stop()
		sweepTick = ticker.C
	}
	if dbTick == nil && sweepTick == nil {
		<-a.stop
		return
	}
	// Close cancels a cleanup that is running.
	stopped, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-a.stop
		cancel()
	}()
	for {
		select {
		case <-a.stop:
			return
		case <-dbTick:
			ctx, cancelRun := context.WithTimeout(stopped, time.Minute)
			if err := a.deleteExpired(ctx); err != nil && stopped.Err() == nil {
				a.logger.WarnContext(ctx, "auth: cannot delete expired sessions and login attempts", "error", err)
			}
			cancelRun()
		case <-sweepTick:
			a.limits.sweep(a.now())
			a.logRejections()
		}
	}
}

type contextKey int

const (
	userContextKey contextKey = iota
	installedContextKey
	limitContextKey
)

// sessionUserKey is the session key that holds the user ID (int64).
const sessionUserKey = "auth.user_id"

// UserFrom returns the user of the request session. The second result is false
// when no user is signed in or when the Auth middleware did not run.
func UserFrom(ctx context.Context) (User, bool) {
	user, ok := ctx.Value(userContextKey).(User)
	return user, ok
}

// Middleware returns the middleware that Routes and RequireUser need. In order, it:
//   - sets Content-Security-Policy to [DefaultCSP] when the response has none,
//   - answers 429 with Retry-After to a client that used up its rate limit for a
//     login, registration, password reset, or OIDC callback request, before any
//     session or database work (see [RateLimits]),
//   - rejects unsafe requests (not GET, HEAD, or OPTIONS) that a browser marks as
//     cross-site or same-site with Sec-Fetch-Site, or whose Origin differs from the
//     Host and the trusted origins, with a 403 problem,
//   - loads the session from the cookie and saves it before the response,
//   - loads the signed-in user, so UserFrom returns it.
func (a *Auth) Middleware() httpx.Middleware {
	return func(next http.Handler) http.Handler {
		inner := a.csrf.Handler(a.sessions.LoadAndSave(a.loadUser(next)))
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if w.Header().Get("Content-Security-Policy") == "" {
				w.Header().Set("Content-Security-Policy", DefaultCSP)
			}
			r = r.WithContext(context.WithValue(r.Context(), installedContextKey, true))
			r, ok := a.allow(w, r)
			if !ok {
				return
			}
			inner.ServeHTTP(&guardWriter{ResponseWriter: w}, r)
		})
	}
}

// RequireUser answers 401 when no user is signed in. Otherwise it calls next.
func (a *Auth) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.installed(w, r) {
			return
		}
		if _, ok := UserFrom(r.Context()); !ok {
			unauthorized(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loadUser puts the user of the session into the request context. A session of a
// deleted user is destroyed.
func (a *Auth) loadUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if id, ok := a.sessions.Get(ctx, sessionUserKey).(int64); ok {
			user, found, err := a.userByID(ctx, id)
			if err != nil {
				a.internalError(w, r, "load the session user", err)
				return
			}
			if found {
				r = r.WithContext(context.WithValue(ctx, userContextKey, user))
			} else if err := a.sessions.Destroy(ctx); err != nil {
				a.internalError(w, r, "destroy the session of a deleted user", err)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// installed answers 500 and returns false when the Auth middleware did not run.
// Without it, scs would panic.
func (a *Auth) installed(w http.ResponseWriter, r *http.Request) bool {
	if r.Context().Value(installedContextKey) != nil {
		return true
	}
	a.logger.ErrorContext(r.Context(), "auth: the auth middleware is not installed; add app.WithMiddleware(authn.Middleware())", "request_id", httpx.RequestIDFrom(r.Context()))
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusInternalServerError})
	return false
}

// sessionError is the scs ErrorFunc. It logs the store failure, writes a 500
// problem, and drops what the handler writes after it.
func (a *Auth) sessionError(w http.ResponseWriter, r *http.Request, err error) {
	a.internalError(w, r, "use the session store", err)
	if g, ok := w.(*guardWriter); ok {
		g.failed = true
	}
}

func (a *Auth) internalError(w http.ResponseWriter, r *http.Request, action string, err error) {
	a.logger.ErrorContext(r.Context(), "auth: cannot "+action, "error", err, "request_id", httpx.RequestIDFrom(r.Context()))
	noStore(w)
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusInternalServerError})
}

// guardWriter discards the handler output after a session store failure, because
// the failure response is already written.
type guardWriter struct {
	http.ResponseWriter
	failed bool
}

func (g *guardWriter) WriteHeader(status int) {
	if !g.failed {
		g.ResponseWriter.WriteHeader(status)
	}
}

func (g *guardWriter) Write(data []byte) (int, error) {
	if g.failed {
		return len(data), nil
	}
	return g.ResponseWriter.Write(data)
}

func (g *guardWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }

func unauthorized(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusUnauthorized, Detail: "No user is signed in."})
}
