package dofake

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// Options configures a fake server. Zero values take the defaults.
type Options struct {
	IPv4          string        // address of new droplets (203.0.113.10)
	IPv6          string        // IPv6 address of new droplets (2001:db8::10)
	ActivateAfter int           // GETs of a droplet before it is active (1)
	ClientID      string        // accepted OAuth client (spinup-test-client)
	Deny          bool          // the authorize endpoint answers access_denied
	Email         string        // account email (dev@example.com)
	TeamUUID      string        // account team UUID
	TeamName      string        // account team name
	DropletLimit  int           // droplet limit (10)
	StaticTokens  []string      // personal access tokens that the API accepts
	AccessTTL     time.Duration // lifetime of an access token (1h)
	Now           func() time.Time
	// OnCreateDroplet runs with the decoded body of a droplet create. An
	// error fails the create with HTTP 422.
	OnCreateDroplet func(req map[string]any) error
}

// Request is one logged request. Path includes the raw query. No token is logged.
type Request struct {
	Method string
	Path   string
}

type failRule struct {
	method, prefix string
	status, n      int
}

// Server is an in-memory model of the DigitalOcean API and OAuth endpoints.
type Server struct {
	o  Options
	ts *httptest.Server

	mu          sync.Mutex
	nextID      int
	droplets    []*dropletState
	firewalls   []map[string]any
	tags        []string
	sshKeys     []SSHKey
	domains     []string
	records     map[string][]Record
	requests    []Request
	fails       []failRule
	rateLimited int
	codes       map[string]*authCode
	access      map[string]*grant
	refresh     map[string]*grant
	issued      int
}

// New returns a fake server.
func New(o Options) *Server {
	def := func(s *string, v string) {
		if *s == "" {
			*s = v
		}
	}
	def(&o.IPv4, "203.0.113.10")
	def(&o.IPv6, "2001:db8::10")
	def(&o.ClientID, "spinup-test-client")
	def(&o.Email, "dev@example.com")
	if o.ActivateAfter == 0 {
		o.ActivateAfter = 1
	}
	if o.DropletLimit == 0 {
		o.DropletLimit = 10
	}
	if o.AccessTTL == 0 {
		o.AccessTTL = time.Hour
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Server{
		o: o, nextID: 1000, records: map[string][]Record{},
		codes: map[string]*authCode{}, access: map[string]*grant{}, refresh: map[string]*grant{},
	}
}

// Handler returns the HTTP handler. It serves /v2/ and /v1/oauth/.
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serve) }

// Start starts an httptest server and returns its URL.
func (s *Server) Start() string {
	s.ts = httptest.NewServer(s.Handler())
	return s.ts.URL
}

// Close stops the httptest server.
func (s *Server) Close() {
	if s.ts != nil {
		s.ts.Close()
	}
}

// FailNext makes the next n requests that match the method and path prefix
// answer with the status. An empty method matches every method.
func (s *Server) FailNext(method, pathPrefix string, status, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fails = append(s.fails, failRule{method, pathPrefix, status, n})
}

// RateLimit makes the next n requests answer 429 with Retry-After: 1.
func (s *Server) RateLimit(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rateLimited = n
}

// AddDomain adds a DNS zone.
func (s *Server) AddDomain(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.domains = append(s.domains, strings.ToLower(name))
}

// Requests returns the request log.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// TokensIssued returns how many access tokens the OAuth endpoints issued.
func (s *Server) TokensIssued() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.issued
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	r.Body = io.NopCloser(bytes.NewReader(body))
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("x-request-id", randHex(8))
	s.requests = append(s.requests, Request{r.Method, r.URL.RequestURI()})
	if s.rateLimited > 0 {
		s.rateLimited--
		w.Header().Set("Retry-After", "1")
		apiError(w, 429, "too_many_requests", "rate limit reached")
		return
	}
	for i := range s.fails {
		f := &s.fails[i]
		if f.n > 0 && (f.method == "" || f.method == r.Method) && strings.HasPrefix(r.URL.Path, f.prefix) {
			f.n--
			apiError(w, f.status, "server_error", "injected failure")
			return
		}
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/v1/oauth/"):
		s.oauth(w, r, body)
	case strings.HasPrefix(r.URL.Path, "/v2/"):
		s.api(w, r, body)
	default:
		apiError(w, 404, "not_found", "The resource you requested could not be found.")
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, id, msg string) {
	writeJSON(w, status, map[string]string{"id": id, "message": msg})
}

func notFound(w http.ResponseWriter) {
	apiError(w, 404, "not_found", "The resource you requested could not be found.")
}

// clone copies a JSON-like value by an encode and decode.
func clone[T any](v T) T {
	var out T
	b, _ := json.Marshal(v)
	_ = json.Unmarshal(b, &out)
	return out
}
