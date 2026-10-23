package auth

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/netip"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/stopyellingatme/spinup/kit/config"
	"github.com/stopyellingatme/spinup/kit/httpx"
)

// Limit is one rate limit for one kind of request: Burst requests at once, then
// one more request each Every. A zero Burst or Every keeps the default of the
// class. Off turns the class off.
type Limit struct {
	Burst int
	Every time.Duration
	Off   bool
}

// RateLimits holds the per-client limits of the auth endpoints (ADR 0016).
// A zero field keeps the default. The defaults are:
//
//	Login         burst 20,  every 3s    POST /api/auth/login
//	Register      burst 20,  every 20s   POST /api/auth/register
//	ResetRequest  burst 10,  every 30s   POST /api/auth/reset/request
//	ResetConfirm  burst 10,  every 30s   POST /api/auth/reset/confirm
//	OIDCCallback  burst 100, every 1s    GET /api/auth/oidc/{provider}/callback
//
// Clients is the number of client entries that the API keeps in memory, for all
// classes together. The default is 65536.
type RateLimits struct {
	Login, Register, ResetRequest, ResetConfirm, OIDCCallback Limit
	Clients                                                   int
}

// WithRateLimits sets the per-client limits of the auth endpoints. A field that
// the call sets wins over the environment (AUTH_RATE_LIMIT and
// AUTH_RATE_LIMIT_<CLASS>). A non-zero Burst or Every in a Limit turns the
// class on, also when the environment says off.
func WithRateLimits(r RateLimits) Option { return func(o *options) { o.rates = r } }

// WithTrustedProxies sets the addresses of the reverse proxies that the API
// trusts to write X-Forwarded-For. An item is a CIDR, a bare address, or one of
// the words private, loopback, and none. Without items, the API trusts nobody.
// The call wins over AUTH_TRUSTED_PROXIES. The default is private.
func WithTrustedProxies(items ...string) Option {
	return func(o *options) { o.proxies, o.proxiesSet = items, true }
}

// limitClass is a kind of limited request. Each class has its own buckets.
type limitClass uint8

const (
	classLogin limitClass = iota
	classRegister
	classResetRequest
	classResetConfirm
	classOIDCCallback
	numClasses
)

type classInfo struct {
	name   string // the metric attribute and the log field
	env    string // the suffix of the AUTH_RATE_LIMIT_ variable
	action string // the text of the 429 detail
	def    Limit
	infra  Limit // zero Every means "scale with GOMAXPROCS"
}

var classes = [numClasses]classInfo{
	classLogin:        {"login", "LOGIN", "sign-in attempts", Limit{20, 3 * time.Second, false}, Limit{Burst: 50}},
	classRegister:     {"register", "REGISTER", "sign-up attempts", Limit{20, 20 * time.Second, false}, Limit{Burst: 50}},
	classResetRequest: {"reset_request", "RESET_REQUEST", "password reset requests", Limit{10, 30 * time.Second, false}, Limit{Burst: 50}},
	classResetConfirm: {"reset_confirm", "RESET_CONFIRM", "password reset attempts", Limit{10, 30 * time.Second, false}, Limit{Burst: 50}},
	classOIDCCallback: {"oidc_callback", "OIDC_CALLBACK", "sign-in attempts", Limit{100, time.Second, false}, Limit{200, 50 * time.Millisecond, false}},
}

// The limited routes. Routes, ResetRoutes, and the middleware read these names, so the
// patterns of the mux and the check cannot drift apart.
const (
	pathRegister     = "/api/auth/register"
	pathLogin        = "/api/auth/login"
	pathResetRequest = "/api/auth/reset/request"
	pathResetConfirm = "/api/auth/reset/confirm"

	oidcPrefix          = "/api/auth/oidc/"
	oidcCallbackSuffix  = "/callback"
	patternOIDCCallback = "GET " + oidcPrefix + "{provider}" + oidcCallbackSuffix
)

var limitedPaths = map[string]limitClass{
	pathRegister:     classRegister,
	pathLogin:        classLogin,
	pathResetRequest: classResetRequest,
	pathResetConfirm: classResetConfirm,
}

// routeClass returns the class of a request that the rate limit covers. All
// limited routes except the OIDC callback are POST routes with an exact path.
func routeClass(r *http.Request) (limitClass, bool) {
	if r.Method == http.MethodPost {
		c, ok := limitedPaths[r.URL.Path]
		return c, ok
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		// The mux matches {provider} on the escaped path, and so does this check.
		p := r.URL.EscapedPath()
		if rest, ok := strings.CutPrefix(p, oidcPrefix); ok {
			if provider, ok := strings.CutSuffix(rest, oidcCallbackSuffix); ok && provider != "" && !strings.Contains(provider, "/") {
				return classOIDCCallback, true
			}
		}
	}
	return 0, false
}

// rule is the GCRA form of a Limit. All fields are nanoseconds.
type rule struct {
	t    int64 // the emission interval (Every)
	tau  int64 // the tolerance: (Burst-1) * Every
	full int64 // Burst * Every
	off  bool
}

func newRule(burst int, every time.Duration, off bool) rule {
	t := int64(every)
	return rule{t: t, tau: int64(burst-1) * t, full: int64(burst) * t, off: off}
}

// limitKey names one bucket. level 0 is the address (IPv4) or the /64 (IPv6),
// level 1 is the /48 of an IPv6 address, and level 2 is the shared bucket of
// the infrastructure.
type limitKey struct {
	addr  netip.Addr
	class limitClass
	level uint8
}

const (
	levelHost  = 0
	levelWide  = 1
	levelInfra = 2

	// wideFactor is the budget of a /48 bucket as a multiple of a /64 bucket.
	wideFactor = 8
	// evictSample is the number of entries that an eviction reads.
	evictSample = 16
	// sweepChunk is the number of entries that a sweep reads before it lets
	// other requests take the lock.
	sweepChunk = 4096

	defaultClients = 65536
	minClients     = 1024
	maxClients     = 4194304

	maxBurst = 10000
	minEvery = time.Millisecond
	maxEvery = 24 * time.Hour
	maxSpan  = 7 * 24 * time.Hour
)

// limiter holds one GCRA state (the theoretical arrival time) per bucket. The
// table never holds more than max entries.
type limiter struct {
	mu  sync.Mutex
	tat map[limitKey]int64
	max int

	host  [numClasses]rule
	wide  [numClasses]rule
	infra [numClasses]rule

	rejected [numClasses]atomic.Int64
	counter  metric.Int64Counter
}

func newLimiter(rates [numClasses]Limit, clients, procs int) *limiter {
	l := &limiter{tat: make(map[limitKey]int64), max: clients}
	for c := range numClasses {
		r := rates[c]
		l.host[c] = newRule(r.Burst, r.Every, r.Off)
		// The /48 bucket has 8 times the budget of the /64 bucket.
		l.wide[c] = newRule(r.Burst*wideFactor, r.Every/wideFactor, r.Off)
		inf := classes[c].infra
		if inf.Every == 0 {
			// About twice the hash capacity of the machine: one hash takes 0.16 CPU-seconds.
			inf.Every = max(minEvery, 320*time.Millisecond/time.Duration(max(1, procs)))
		}
		l.infra[c] = newRule(inf.Burst, inf.Every, r.Off)
	}
	if counter, err := otel.Meter("github.com/stopyellingatme/spinup/kit/auth").Int64Counter("auth.rate_limited",
		metric.WithDescription("Auth requests that the per-client rate limit refused.")); err == nil {
		l.counter = counter
	}
	return l
}

// taken records the buckets that one request took a token from, so that a
// request which the hash queue refuses can give the token back.
type taken struct {
	keys [2]limitKey
	step [2]int64
	n    int
	done atomic.Bool
}

// take reports whether one request of class c is allowed. When it is not, wait
// is the time to the next token, and no bucket changes. An IPv6 client takes a
// token from its /64 bucket and from its /48 bucket, in one lock.
func (l *limiter) take(c limitClass, who client, now time.Time) (tk *taken, wait time.Duration, ok bool) {
	var keys [2]limitKey
	var rules [2]rule
	n := 1
	switch {
	case who.kind == kindInfra:
		keys[0], rules[0] = limitKey{class: c, level: levelInfra}, l.infra[c]
	case who.addr.Is4():
		keys[0], rules[0] = limitKey{addr: who.addr, class: c, level: levelHost}, l.host[c]
	default:
		keys[0], rules[0] = limitKey{addr: maskAddr(who.addr, 64), class: c, level: levelHost}, l.host[c]
		keys[1], rules[1] = limitKey{addr: maskAddr(who.addr, 48), class: c, level: levelWide}, l.wide[c]
		n = 2
	}
	if rules[0].off {
		return nil, 0, true
	}
	ns := now.UnixNano()
	var next [2]int64
	l.mu.Lock()
	defer l.mu.Unlock()
	ok = true
	for i := range n {
		tat := max(l.tat[keys[i]], ns)
		// A clock that stepped back cannot lock a key for longer than one full bucket.
		tat = min(tat, ns+rules[i].full)
		if tat-ns > rules[i].tau {
			ok = false
			wait = max(wait, time.Duration(tat-ns-rules[i].tau))
			continue
		}
		next[i] = tat + rules[i].t
	}
	if !ok {
		l.rejected[c].Add(1)
		return nil, wait, false
	}
	tk = &taken{keys: keys, n: n}
	for i := range n {
		tk.step[i] = rules[i].t
		l.putLocked(keys[i], next[i], ns)
	}
	return tk, 0, true
}

func (l *limiter) putLocked(k limitKey, tat, now int64) {
	if _, found := l.tat[k]; !found && len(l.tat) >= l.max {
		l.evictLocked(now)
	}
	l.tat[k] = tat
}

// evictLocked frees one place. It reads a sample of entries. An entry whose bucket is
// full (tat <= now) equals no entry, so it goes first. Without one, the entry
// with the smallest tat goes, because it has the shortest penalty left.
func (l *limiter) evictLocked(now int64) {
	var victim limitKey
	least := int64(math.MaxInt64)
	n := 0
	for k, tat := range l.tat {
		if tat <= now {
			delete(l.tat, k)
			return
		}
		if tat < least {
			victim, least = k, tat
		}
		if n++; n >= evictSample {
			break
		}
	}
	delete(l.tat, victim)
}

// refund gives back the tokens of a request.
func (l *limiter) refund(tk *taken, now time.Time) {
	if tk == nil || !tk.done.CompareAndSwap(false, true) {
		return
	}
	ns := now.UnixNano()
	l.mu.Lock()
	defer l.mu.Unlock()
	for i := range tk.n {
		tat, found := l.tat[tk.keys[i]]
		if !found {
			continue
		}
		if tat -= tk.step[i]; tat <= ns {
			delete(l.tat, tk.keys[i])
		} else {
			l.tat[tk.keys[i]] = tat
		}
	}
}

// sweep deletes the entries whose bucket is full. It lets other requests take the
// lock after each chunk of entries.
func (l *limiter) sweep(now time.Time) (removed int) {
	ns := now.UnixNano()
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for k, tat := range l.tat {
		if tat <= ns {
			delete(l.tat, k)
			removed++
		}
		if n++; n%sweepChunk == 0 {
			l.mu.Unlock()
			runtime.Gosched()
			l.mu.Lock()
		}
	}
	return removed
}

func (l *limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.tat)
}

// maskAddr returns the first bits of an IPv6 address, with the other bits zero.
func maskAddr(a netip.Addr, bits int) netip.Addr {
	p, err := a.Prefix(bits)
	if err != nil {
		return a
	}
	return p.Addr()
}

// rateSettings are the environment variables of the rate limit. The kit reads
// them with config.LoadFrom and a lookup function, like the OIDC settings. It
// does not read a .env file.
type rateSettings struct {
	Switch       string `env:"AUTH_RATE_LIMIT"`
	Login        string `env:"AUTH_RATE_LIMIT_LOGIN"`
	Register     string `env:"AUTH_RATE_LIMIT_REGISTER"`
	ResetRequest string `env:"AUTH_RATE_LIMIT_RESET_REQUEST"`
	ResetConfirm string `env:"AUTH_RATE_LIMIT_RESET_CONFIRM"`
	OIDCCallback string `env:"AUTH_RATE_LIMIT_OIDC_CALLBACK"`
	Clients      int    `env:"AUTH_RATE_LIMIT_CLIENTS"`
	Proxies      string `env:"AUTH_TRUSTED_PROXIES"`
}

func (s rateSettings) spec(c limitClass) (name, value string) {
	switch c {
	case classLogin:
		return "AUTH_RATE_LIMIT_LOGIN", s.Login
	case classRegister:
		return "AUTH_RATE_LIMIT_REGISTER", s.Register
	case classResetRequest:
		return "AUTH_RATE_LIMIT_RESET_REQUEST", s.ResetRequest
	case classResetConfirm:
		return "AUTH_RATE_LIMIT_RESET_CONFIRM", s.ResetConfirm
	default:
		return "AUTH_RATE_LIMIT_OIDC_CALLBACK", s.OIDCCallback
	}
}

func (r RateLimits) limit(c limitClass) Limit {
	return [numClasses]Limit{r.Login, r.Register, r.ResetRequest, r.ResetConfirm, r.OIDCCallback}[c]
}

// loadRateSettings reads the environment through lookup.
func loadRateSettings(lookup func(string) (string, bool)) (rateSettings, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	var s rateSettings
	if err := config.LoadFrom(&s, lookup, os.ReadFile); err != nil {
		return s, fmt.Errorf("auth: cannot start: %w", err)
	}
	return s, nil
}

func envError(name, value, why, try string) error {
	return fmt.Errorf("auth: cannot start: %s %q is not valid: %s\nTry: %s", name, value, why, try)
}

// parseSwitch reads the value of AUTH_RATE_LIMIT.
func parseSwitch(s string) (off bool, err error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "on", "true", "1":
		return false, nil
	case "off", "false", "0":
		return true, nil
	}
	return false, envError("AUTH_RATE_LIMIT", s, "use on or off", "set AUTH_RATE_LIMIT=off to turn the rate limit off, or remove it to use the default")
}

// parseLimitSpec reads "off" or "burst=<n>,every=<duration>". Each key is optional.
func parseLimitSpec(s string) (Limit, error) {
	var l Limit
	s = strings.TrimSpace(s)
	if s == "" {
		return l, nil
	}
	if strings.EqualFold(s, "off") {
		return Limit{Off: true}, nil
	}
	for part := range strings.SplitSeq(s, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		switch {
		case !ok:
			return l, errors.New("use off or burst=<n>,every=<duration>")
		case key == "burst" && l.Burst == 0:
			n, err := strconv.Atoi(value)
			if err != nil {
				return l, errors.New("burst must be a whole number")
			}
			l.Burst = n
			if n < 1 || n > maxBurst {
				return l, fmt.Errorf("burst must be 1 to %d", maxBurst)
			}
		case key == "every" && l.Every == 0:
			d, err := time.ParseDuration(value)
			if err != nil {
				return l, errors.New("every must be a duration such as 3s")
			}
			l.Every = d
			if d < minEvery || d > maxEvery {
				return l, fmt.Errorf("every must be %s to %s", minEvery, maxEvery)
			}
		default:
			return l, fmt.Errorf("the key %q is unknown or repeated", key)
		}
	}
	return l, nil
}

// resolveRates merges the defaults, the environment, and the code option, in that
// order of precedence: the option wins over the environment, and the environment wins
// over the default.
func resolveRates(opt RateLimits, s rateSettings) (rates [numClasses]Limit, clients int, err error) {
	allOff, err := parseSwitch(s.Switch)
	if err != nil {
		return rates, 0, err
	}
	for c := range numClasses {
		info := classes[c]
		l := info.def
		name, raw := s.spec(c)
		fromEnv, err := parseLimitSpec(raw)
		if err != nil {
			return rates, 0, envError(name, raw, err.Error(), fmt.Sprintf("set %s=burst=%d,every=%s, or remove it to use the default", name, info.def.Burst, info.def.Every))
		}
		l = overlay(l, fromEnv)
		if allOff {
			l.Off = true
		}
		o := opt.limit(c)
		if o.Burst < 0 || o.Every < 0 {
			return rates, 0, fmt.Errorf("auth: cannot start: the %s limit has a negative burst or interval\nTry: pass a positive Burst and Every to auth.WithRateLimits", info.name)
		}
		l = overlay(l, o)
		if !l.Off {
			if l.Burst < 1 || l.Burst > maxBurst || l.Every < minEvery || l.Every > maxEvery || time.Duration(l.Burst)*l.Every > maxSpan {
				return rates, 0, fmt.Errorf("auth: cannot start: the %s limit burst=%d,every=%s is not valid: burst must be 1 to %d, every must be %s to %s, and burst times every at most %s\nTry: correct %s or the values of auth.WithRateLimits",
					info.name, l.Burst, l.Every, maxBurst, minEvery, maxEvery, maxSpan, name)
			}
		}
		rates[c] = l
	}
	clients = defaultClients
	if s.Clients != 0 {
		clients = s.Clients
		if clients < minClients || clients > maxClients {
			return rates, 0, envError("AUTH_RATE_LIMIT_CLIENTS", strconv.Itoa(clients), fmt.Sprintf("use %d to %d", minClients, maxClients), "set AUTH_RATE_LIMIT_CLIENTS=65536, or remove it to use the default")
		}
	}
	if opt.Clients != 0 {
		clients = opt.Clients
		if clients < minClients || clients > maxClients {
			return rates, 0, fmt.Errorf("auth: cannot start: RateLimits.Clients %d is out of range\nTry: use %d to %d in auth.WithRateLimits", clients, minClients, maxClients)
		}
	}
	return rates, clients, nil
}

// overlay applies the fields that over sets. A non-zero Burst or Every turns the
// class on, unless over turns it off.
func overlay(base, over Limit) Limit {
	if over.Burst != 0 {
		base.Burst, base.Off = over.Burst, false
	}
	if over.Every != 0 {
		base.Every, base.Off = over.Every, false
	}
	if over.Off {
		base.Off = true
	}
	return base
}

// allow runs the per-client check of a request that Middleware receives. It
// returns false after it wrote the 429 answer. A request that the check covers
// carries the tokens it took, so that busy can give them back.
func (a *Auth) allow(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	if a.limits == nil {
		return r, true
	}
	c, ok := routeClass(r)
	if !ok {
		return r, true
	}
	who := a.proxies.resolve(r)
	if who.kind == kindExempt {
		return r, true
	}
	if who.kind == kindInfra {
		a.warnInfrastructure(r)
	}
	now := a.now()
	tk, wait, ok := a.limits.take(c, who, now)
	if !ok {
		a.tooMany(w, r, c, wait)
		return r, false
	}
	if tk != nil {
		r = r.WithContext(context.WithValue(r.Context(), limitContextKey, tk))
	}
	return r, true
}

// refundLimit gives back the tokens of r. A request that the hash queue refuses
// did no password work, so it must not use up the budget of an honest client.
func (a *Auth) refundLimit(r *http.Request) {
	if a.limits == nil {
		return
	}
	if tk, ok := r.Context().Value(limitContextKey).(*taken); ok {
		a.limits.refund(tk, a.now())
	}
}

// tooMany answers a request that the rate limit refused.
func (a *Auth) tooMany(w http.ResponseWriter, r *http.Request, c limitClass, wait time.Duration) {
	seconds := max(1, int(math.Ceil(wait.Seconds())))
	noStore(w)
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	if a.limits.counter != nil {
		a.limits.counter.Add(r.Context(), 1, metric.WithAttributes(attribute.String("class", classes[c].name)))
	}
	if c == classOIDCCallback {
		// The callback is a browser navigation. A problem page would end the sign-in
		// flow in raw JSON, so go to the login page with an error code.
		http.Redirect(w, r, "/login?error=rate_limited", http.StatusSeeOther)
		return
	}
	unit := "seconds"
	if seconds == 1 {
		unit = "second"
	}
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusTooManyRequests,
		Detail: fmt.Sprintf("Too many %s from your network. Try again in %d %s.", classes[c].action, seconds, unit)})
}

const (
	logSweepInterval = time.Minute
	warnInterval     = 10 * time.Minute
)

// warnInfrastructure logs at most once in warnInterval that requests share the
// infrastructure bucket. The log has no address.
func (a *Auth) warnInfrastructure(r *http.Request) {
	now := a.now().UnixNano()
	last := a.warnedAt.Load()
	if last != 0 && now-last < int64(warnInterval) || !a.warnedAt.CompareAndSwap(last, now) {
		return
	}
	a.logger.WarnContext(r.Context(), "auth: requests from a trusted proxy have no client address, so they share one rate limit",
		"request_id", httpx.RequestIDFrom(r.Context()),
		"try", "make the web service send X-Forwarded-For (ADDRESS_HEADER=X-Forwarded-For), or set AUTH_TRUSTED_PROXIES")
}

// logRejections writes one line when the last interval had refusals. It never
// writes an address.
func (a *Auth) logRejections() {
	if a.limits == nil {
		return
	}
	var attrs []any
	total := int64(0)
	for c := range numClasses {
		if n := a.limits.rejected[c].Swap(0); n > 0 {
			attrs = append(attrs, classes[c].name, n)
			total += n
		}
	}
	if total == 0 {
		return
	}
	attrs = append(attrs, "try", "raise AUTH_RATE_LIMIT_<CLASS> if real users hit the limit, or set AUTH_TRUSTED_PROXIES if all clients show one address")
	a.logger.Warn("auth: rate limited requests in the last minute", attrs...)
}
