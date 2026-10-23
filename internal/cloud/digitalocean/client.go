package digitalocean

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxAttempts  = 5
	maxBody      = 4 << 20
	maxPages     = 1000
	maxRateWait  = 2 * time.Minute
	backoffStart = 500 * time.Millisecond
)

// Client is a DigitalOcean API v2 client.
// Token runs before each request, so the token can refresh between calls.
type Client struct {
	Base      string
	HTTP      *http.Client
	Token     func(context.Context) (string, error)
	Sleep     func(context.Context, time.Duration) error
	Now       func() time.Time
	UserAgent string
}

// New returns a client for the API at base.
func New(base string, token func(context.Context) (string, error)) *Client {
	return &Client{
		Base:      strings.TrimRight(base, "/"),
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		Token:     token,
		Sleep:     sleepCtx,
		Now:       time.Now,
		UserAgent: "spinup",
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	return sleepCtx(ctx, d)
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func withQuery(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

func pathOnly(target string) string {
	p, _, _ := strings.Cut(target, "?")
	return p
}

// fatalError marks an error that no retry can fix.
type fatalError struct{ err error }

func (e *fatalError) Error() string { return e.err.Error() }
func (e *fatalError) Unwrap() error { return e.err }

// do sends one logical request. It retries by the rules of ADR 0017 section 3.
func (c *Client) do(ctx context.Context, method, target string, in, out any) error {
	var body []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("cannot encode the request for %s %s: %w\nTry: report this error", method, pathOnly(target), err)
		}
		body = b
	}
	idempotent := method == http.MethodGet || method == http.MethodPut || method == http.MethodDelete
	for attempt := 1; ; attempt++ {
		status, hdr, data, err := c.once(ctx, method, target, body)
		last := attempt >= maxAttempts
		var fe *fatalError
		switch {
		case errors.As(err, &fe):
			return fe.err
		case err != nil:
			if ctx.Err() != nil || !idempotent || last {
				return err
			}
			if serr := c.sleep(ctx, backoff(attempt)); serr != nil {
				return serr
			}
			continue
		case status == http.StatusTooManyRequests && method != http.MethodPost && !last:
			if serr := c.sleep(ctx, c.rateWait(hdr, attempt)); serr != nil {
				return serr
			}
			continue
		case status >= 500 && idempotent && !last:
			if serr := c.sleep(ctx, backoff(attempt)); serr != nil {
				return serr
			}
			continue
		case status < 200 || status > 299:
			return parseError(status, hdr, data, method, target)
		}
		if out != nil && len(bytes.TrimSpace(data)) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("cannot read the answer of %s %s: %w\nTry: check status.digitalocean.com and run the command again", method, pathOnly(target), err)
			}
		}
		return nil
	}
}

func (c *Client) once(ctx context.Context, method, target string, body []byte) (int, http.Header, []byte, error) {
	tok, err := c.Token(ctx)
	if err != nil {
		return 0, nil, nil, &fatalError{err}
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+target, rd)
	if err != nil {
		return 0, nil, nil, &fatalError{fmt.Errorf("cannot build the request %s %s: %w\nTry: check the API base URL", method, pathOnly(target), err)}
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, nil, &fatalError{ctx.Err()}
		}
		return 0, nil, nil, fmt.Errorf("cannot reach the DigitalOcean API for %s %s: %w\nTry: check the network connection and run the command again", method, pathOnly(target), err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("cannot read the answer of %s %s: %w\nTry: check the network connection and run the command again", method, pathOnly(target), err)
	}
	if len(data) > maxBody {
		return 0, nil, nil, &fatalError{fmt.Errorf("the answer of %s %s is larger than %d bytes\nTry: check status.digitalocean.com and run the command again", method, pathOnly(target), maxBody)}
	}
	return resp.StatusCode, resp.Header, data, nil
}

func parseError(status int, hdr http.Header, data []byte, method, target string) error {
	e := &APIError{Status: status, Method: method, Path: pathOnly(target), RequestID: hdr.Get("x-request-id")}
	var body struct {
		ID      string `json:"id"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &body) == nil {
		e.ID, e.Message = body.ID, body.Message
	}
	return e
}

// backoff gives a jittered exponential delay for the attempt (1 based).
func backoff(attempt int) time.Duration {
	d := backoffStart << (attempt - 1)
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// rateWait reads Retry-After (seconds), then RateLimit-Reset (epoch seconds).
func (c *Client) rateWait(hdr http.Header, attempt int) time.Duration {
	var d time.Duration
	if n, err := strconv.Atoi(strings.TrimSpace(hdr.Get("Retry-After"))); err == nil {
		d = time.Duration(n) * time.Second
	} else if n, err := strconv.ParseInt(strings.TrimSpace(hdr.Get("RateLimit-Reset")), 10, 64); err == nil {
		d = time.Unix(n, 0).Sub(c.now())
	} else {
		d = backoff(attempt)
	}
	return min(max(d, 0), maxRateWait)
}

// listAll reads every page of a list endpoint. It follows links.pages.next
// only when the link has the same host as the base.
func listAll[T any](ctx context.Context, c *Client, path string, q url.Values, key string) ([]T, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("per_page", "200")
	baseURL, err := url.Parse(c.Base)
	if err != nil {
		return nil, fmt.Errorf("cannot use the API base %q: %w\nTry: check SPINUP_DO_API_BASE", c.Base, err)
	}
	var all []T
	target := withQuery(path, q)
	for page := 0; target != ""; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("the list %s has more than %d pages\nTry: report this error", path, maxPages)
		}
		var raw json.RawMessage
		if err := c.do(ctx, http.MethodGet, target, nil, &raw); err != nil {
			return nil, err
		}
		var env map[string]json.RawMessage
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("cannot read the list %s: %w\nTry: check status.digitalocean.com and run the command again", path, err)
		}
		var items []T
		if v, ok := env[key]; ok {
			if err := json.Unmarshal(v, &items); err != nil {
				return nil, fmt.Errorf("cannot read %q in the list %s: %w\nTry: check status.digitalocean.com and run the command again", key, path, err)
			}
		}
		all = append(all, items...)
		var links struct {
			Pages struct {
				Next string `json:"next"`
			} `json:"pages"`
		}
		if v, ok := env["links"]; ok {
			_ = json.Unmarshal(v, &links)
		}
		target = ""
		if next := links.Pages.Next; next != "" {
			// A link to another host ends the list.
			if u, perr := url.Parse(next); perr == nil && strings.EqualFold(u.Host, baseURL.Host) {
				target = strings.TrimPrefix(u.RequestURI(), strings.TrimRight(baseURL.Path, "/"))
			}
		}
	}
	return all, nil
}
