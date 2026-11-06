package app

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/kit/config"
)

type pipeListener struct {
	conn   net.Conn
	taken  bool
	mu     sync.Mutex
	closed chan struct{}
	once   sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.taken {
		l.taken = true
		l.mu.Unlock()
		return l.conn, nil
	}
	l.mu.Unlock()
	<-l.closed
	return nil, net.ErrClosed
}
func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *pipeListener) Addr() net.Addr { return l.conn.LocalAddr() }
func TestExemptTransportDeadline(t *testing.T) {
	for _, path := range []string{"/events", "/_dev", "/_dev/requests.json"} {
		t.Run(path, func(t *testing.T) { testExemptTransportDeadline(t, path) })
	}
}

func testExemptTransportDeadline(t *testing.T, path string) {
	t.Helper()
	server, client := net.Pipe()
	defer client.Close()
	listener := &pipeListener{conn: server, closed: make(chan struct{})}
	app := New(WithLogger(discardLogger()), WithRequestTimeout(20*time.Millisecond), WithConfig(config.Base{Env: "dev"}),
		WithRequestTimeoutExempt(func(r *http.Request) bool { return r.URL.Path == "/events" }),
		WithMiddleware(func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if _, ok := r.Context().Deadline(); ok {
					t.Error("exempt request has a context deadline")
				}
				time.Sleep(80 * time.Millisecond)
				_, _ = io.WriteString(w, "event: ping\n")
			})
		}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.serveListener(ctx, listener) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	_ = client.SetDeadline(time.Now().Add(time.Second))
	if _, err := io.WriteString(client, "GET "+path+" HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatalf("exempt request failed after 80ms with REQUEST_TIMEOUT=20ms: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "event: ping\n" {
		t.Fatalf("exempt response=%q error=%v", body, err)
	}
}
