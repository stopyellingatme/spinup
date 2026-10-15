package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// healthcheck is the built-in "healthcheck" command. Container images without a shell or
// curl (distroless) use it as their Docker health check: it asks the running server on the
// same container for /healthz and fails unless it answers 200 within two seconds.
func (a *App) healthcheck(ctx context.Context) error {
	_, port, err := net.SplitHostPort(a.addr)
	if err != nil || port == "" {
		return fmt.Errorf("healthcheck: cannot read the port from %q\nTry: set PORT or WithAddr", a.addr)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: %w\nTry: check that the server is running on port %s", err, port)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: /healthz answered %d\nTry: read the server logs", resp.StatusCode)
	}
	return nil
}
