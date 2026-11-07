package stripe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type options struct {
	baseURL string
	http    *http.Client
	retries int
	wait    func(context.Context, time.Duration) error
}

// Option configures the Stripe client.
type Option func(*options)

// WithBaseURL selects an API origin for tests or STRIPE_API_BASE.
func WithBaseURL(u string) Option { return func(o *options) { o.baseURL = strings.TrimRight(u, "/") } }

// WithHTTPClient selects the HTTP client. The default timeout is 20 seconds.
func WithHTTPClient(c *http.Client) Option { return func(o *options) { o.http = c } }

// WithRetries sets the retry count. The default is two retries.
func WithRetries(n int) Option { return func(o *options) { o.retries = n } }

// Client calls the Stripe API at the pinned version.
type Client struct {
	key string
	o   options
}

// New validates the key and creates a client without network access.
func New(secretKey string, opts ...Option) (*Client, error) {
	if strings.TrimSpace(secretKey) == "" {
		return nil, fmt.Errorf("create Stripe client: the secret key is blank\nTry: set STRIPE_SECRET_KEY")
	}
	o := options{baseURL: DefaultBaseURL, http: &http.Client{Timeout: 20 * time.Second}, retries: 2, wait: wait}
	for _, opt := range opts {
		opt(&o)
	}
	u, err := url.Parse(o.baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("create Stripe client: invalid API base URL\nTry: use an HTTP or HTTPS origin")
	}
	if o.http == nil || o.retries < 0 {
		return nil, fmt.Errorf("create Stripe client: invalid HTTP client or retry count\nTry: supply an HTTP client and a nonnegative retry count")
	}
	return &Client{key: secretKey, o: o}, nil
}

// Error describes a failed Stripe API request.
type Error struct {
	// Status is the HTTP response status.
	Status int
	// Type, Code, Param, Message, and RequestID describe the Stripe error.
	Type, Code, Param, Message, RequestID string
	method, path                          string
}

// Error names the failed request and includes the Stripe request ID.
func (e *Error) Error() string {
	return fmt.Sprintf("stripe %s %s failed (status %d): %s (request ID: %s)\nTry: check the Stripe request in the Dashboard and correct the request", e.method, e.path, e.Status, e.Message, e.RequestID)
}

func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) redact(s string) string { return strings.ReplaceAll(s, c.key, "[redacted]") }

func (c *Client) request(ctx context.Context, method, path string, form url.Values, key string, out any) error {
	if method == http.MethodPost && key == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return fmt.Errorf("stripe %s %s failed: cannot create an idempotency key\nTry: check the random source", method, path)
		}
		key = hex.EncodeToString(b[:])
	}
	body := form.Encode() // Values.Encode sorts keys and preserves array order.
	endpoint := c.o.baseURL + path
	if method == http.MethodGet && body != "" {
		endpoint += "?" + body
	}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("stripe %s %s failed: %w\nTry: retry with a live context", method, path, err)
		}
		var reader io.Reader
		if method == http.MethodPost {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
		if err != nil {
			return fmt.Errorf("stripe request failed: invalid URL\nTry: check the API base URL")
		}
		req.Header.Set("Authorization", "Bearer "+c.key)
		req.Header.Set("Stripe-Version", APIVersion)
		req.Header.Set("User-Agent", "spinup-kit-billing")
		if method == http.MethodPost {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Idempotency-Key", key)
		}
		res, sendErr := c.o.http.Do(req)
		var failure error
		retry := false
		if sendErr != nil {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("stripe %s %s failed: %w\nTry: retry with a live context", method, path, err)
			}
			failure = fmt.Errorf("stripe %s %s failed: %s\nTry: check the connection and retry", method, path, c.redact(sendErr.Error()))
			retry = true
		} else {
			data, readErr := io.ReadAll(io.LimitReader(res.Body, 8<<20))
			_ = res.Body.Close()
			switch {
			case readErr != nil:
				failure = fmt.Errorf("stripe %s %s failed: cannot read the response\nTry: check the connection and retry", method, path)
				retry = true
			case res.StatusCode >= 200 && res.StatusCode < 300:
				if out != nil && json.Unmarshal(data, out) != nil {
					return fmt.Errorf("stripe %s %s failed: invalid JSON response\nTry: check the API base URL and response", method, path)
				}
				return nil
			default:
				var envelope struct {
					Error struct{ Type, Code, Param, Message string } `json:"error"`
				}
				_ = json.Unmarshal(data, &envelope)
				e := &Error{Status: res.StatusCode, Type: c.redact(envelope.Error.Type), Code: c.redact(envelope.Error.Code), Param: c.redact(envelope.Error.Param), Message: c.redact(envelope.Error.Message), RequestID: c.redact(res.Header.Get("Request-Id")), method: method, path: c.redact(path)}
				if e.Message == "" {
					e.Message = http.StatusText(e.Status)
				}
				failure = e
				retry = e.Status == http.StatusTooManyRequests || (e.Status >= 500 && e.Status < 600) || (e.Status == http.StatusConflict && e.Code == "lock_timeout")
			}
			switch res.Header.Get("Stripe-Should-Retry") {
			case "true":
				retry = true
			case "false":
				retry = false
			}
		}
		if !retry || attempt >= c.o.retries {
			return failure
		}
		// The first two delays are 500 ms and 1 s, each with 25 percent jitter.
		delay := 500 * time.Millisecond * time.Duration(1<<min(attempt, 4))
		delay = delay*3/4 + time.Duration(mathrand.Int64N(int64(delay/2)+1))
		if err := c.o.wait(ctx, delay); err != nil {
			return fmt.Errorf("stripe %s %s retry failed: %w\nTry: retry with a live context", method, path, err)
		}
	}
}

func metadata(form url.Values, values map[string]string) {
	for k, v := range values {
		form.Set("metadata["+k+"]", v)
	}
}

func list[T any](ctx context.Context, c *Client, path string, form url.Values, id func(T) string) ([]T, error) {
	form.Set("limit", "100")
	var all []T
	for page := 0; page < 10; page++ {
		var result struct {
			Data    []T  `json:"data"`
			HasMore bool `json:"has_more"`
		}
		if err := c.request(ctx, http.MethodGet, path, form, "", &result); err != nil {
			return nil, err
		}
		all = append(all, result.Data...)
		if !result.HasMore {
			return all, nil
		}
		if len(result.Data) == 0 || id(result.Data[len(result.Data)-1]) == "" {
			return nil, fmt.Errorf("stripe GET %s failed: missing pagination cursor\nTry: check the API response", path)
		}
		form.Set("starting_after", id(result.Data[len(result.Data)-1]))
	}
	return nil, fmt.Errorf("stripe GET %s failed: more than 10 pages\nTry: narrow the catalog or customer subscriptions", path)
}
