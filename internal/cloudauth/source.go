package cloudauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Source finds a usable access token. The API client calls Token for each request.
type Source struct {
	Getenv func(string) string
	// EnvNames lists the personal access token variables. The first non-empty one wins.
	EnvNames []string
	// Warn receives one warning when two variables are set and differ.
	Warn        io.Writer
	Store       Store
	Config      Config
	Interactive bool
	// Login runs the browser login. The CLI wires it.
	Login func(ctx context.Context) (Token, error)
	// Margin is the time before expiry when a token counts as expired. The default is 5 minutes.
	Margin  time.Duration
	TryHint string

	mu     sync.Mutex
	cached Token
	warned bool
}

func (s *Source) margin() time.Duration {
	if s.Margin > 0 {
		return s.Margin
	}
	return 5 * time.Minute
}

// Token returns an access token. See Current for the order of the sources.
func (s *Source) Token(ctx context.Context) (string, error) {
	t, _, err := s.Current(ctx)
	return t.AccessToken, err
}

func (s *Source) env() (string, string) {
	if s.Getenv == nil {
		return "", ""
	}
	var first, firstName string
	for _, n := range s.EnvNames {
		v := s.Getenv(n)
		if v == "" {
			continue
		}
		if first == "" {
			first, firstName = v, n
			continue
		}
		if v != first && !s.warned {
			s.warned = true
			if s.Warn != nil {
				_, _ = fmt.Fprintf(s.Warn, "warning: %s and %s are both set and differ. spinup uses %s.\n", firstName, n, firstName)
			}
		}
	}
	return first, firstName
}

func (s *Source) try() string {
	if s.TryHint != "" {
		return s.TryHint
	}
	return "set a personal access token in " + strings.Join(s.EnvNames, " or ") + ", or sign in again"
}

// Current returns the token and where it came from: "env:NAME", "store", or "login".
// The order is: the environment, the stored token (refreshed when needed), then the browser login
// when Interactive is true. The result is cached in memory and checked for expiry on each call.
func (s *Source) Current(ctx context.Context) (Token, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, name := s.env(); v != "" {
		return Token{AccessToken: v}, "env:" + name, nil
	}
	if s.cached.Valid(s.Config.now(), s.margin()) {
		return s.cached, "store", nil
	}
	var res Token
	var from string
	err := s.Store.WithLock(ctx, func() error {
		// Read the file after the lock. Another process can have refreshed the token.
		t, found, err := s.Store.Load()
		if err != nil {
			return err
		}
		if found && t.Valid(s.Config.now(), s.margin()) {
			res, from = t, "store"
			return nil
		}
		if found && t.RefreshToken != "" {
			nt, rerr := Refresh(ctx, s.Config, t.RefreshToken)
			switch {
			case rerr == nil:
				if nt.Info == nil {
					nt.Info = t.Info
				}
				// Refresh tokens are single-use. Save before anything else.
				if err := s.Store.Save(nt); err != nil {
					return err
				}
				res, from = nt, "store"
				return nil
			case errors.Is(rerr, ErrReauth):
				if err := s.Store.Delete(); err != nil {
					return err
				}
			default:
				return rerr
			}
		}
		if s.Interactive && s.Login != nil {
			nt, err := s.Login(ctx)
			if err != nil {
				return err
			}
			if err := s.Store.Save(nt); err != nil {
				return err
			}
			res, from = nt, "login"
			return nil
		}
		return problem("find a sign-in", "no valid token exists and no terminal is available for a sign-in", s.try())
	})
	if err != nil {
		return Token{}, "", err
	}
	s.cached = res
	return res, from, nil
}

// ErrNotRevoked means that Logout deleted the stored token, but DigitalOcean did not confirm the
// revoke. The token can still be valid until it expires.
var ErrNotRevoked = errors.New("the stored token was deleted, but the revoke failed")

// Logout revokes the refresh token and the access token, then deletes the file. When a revoke
// fails, it still deletes the file and returns an error that wraps ErrNotRevoked. When the file
// cannot be read, it keeps the file, so that the user can fix it and revoke later.
func (s *Source) Logout(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cached = Token{}
	return s.Store.WithLock(ctx, func() error {
		t, found, err := s.Store.Load()
		if err != nil {
			return err
		}
		var rerrs []error
		if found {
			if t.RefreshToken != "" {
				if err := Revoke(ctx, s.Config, t.RefreshToken); err != nil {
					rerrs = append(rerrs, err)
				}
			}
			if t.AccessToken != "" {
				if err := Revoke(ctx, s.Config, t.AccessToken); err != nil {
					rerrs = append(rerrs, err)
				}
			}
		}
		if err := s.Store.Delete(); err != nil {
			return errors.Join(append([]error{err}, rerrs...)...)
		}
		if len(rerrs) > 0 {
			return fmt.Errorf("%w: %w", ErrNotRevoked, errors.Join(rerrs...))
		}
		return nil
	})
}
