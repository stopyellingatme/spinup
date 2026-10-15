package mail

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stopyellingatme/spinup/kit/config"
)

func TestRender(t *testing.T) {
	t.Parallel()
	files := fstest.MapFS{
		"welcome.subject.tmpl": {Data: []byte("Hello {{.Name}}\n")},
		"welcome.txt.tmpl":     {Data: []byte("Hello {{.Name}}")},
		"welcome.html.tmpl":    {Data: []byte("<p>Hello {{.Name}}</p>")},
	}
	m, err := Render(files, "welcome", "app@example.com", "you@example.com", struct{ Name string }{"<script>"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Text != "Hello <script>" || m.HTML != "<p>Hello &lt;script&gt;</p>" {
		t.Fatalf("message = %+v", m)
	}
	for _, name := range []string{"../welcome", "missing"} {
		if _, err := Render(files, name, m.From, m.To, nil); err == nil {
			t.Errorf("Render(%q) succeeded", name)
		}
	}
	delete(files, "welcome.txt.tmpl")
	if _, err := Render(files, "welcome", m.From, m.To, nil); err == nil {
		t.Error("missing plain text accepted")
	}
}

func TestCapture(t *testing.T) {
	t.Parallel()
	c := &Capture{}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if err := c.Send(t.Context(), Message{From: "app@example.com", To: "you@example.com", Subject: "Hi", Text: "Hello"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got := c.Messages()
	if len(got) != 20 {
		t.Fatalf("got %d messages", len(got))
	}
	got[0].Text = "changed"
	if c.Messages()[0].Text != "Hello" {
		t.Fatal("capture returned internal storage")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if c.Send(ctx, got[0]) == nil {
		t.Fatal("canceled send succeeded")
	}
}

func TestSMTPConfig(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		url string
		ok  bool
	}{
		{"smtp://localhost:1025", true}, {"smtps://user@example.com:465", true},
		{"smtp+starttls://user@example.com:587", true}, {"smtp://user@example.com:587", true},
		{"http://example.com", false}, {"smtp://user:secret@example.com", false},
		{"smtp://example.com:0", false}, {"smtp://example.com?tls=invalid", false},
		{"smtp://user@example.com?tls=none", false},
	} {
		t.Run(tc.url, func(t *testing.T) {
			t.Parallel()
			_, err := New(Config{URL: tc.url, Password: "secret", Timeout: time.Second})
			if (err == nil) != tc.ok {
				t.Fatalf("error = %v", err)
			}
			if err != nil && (!strings.Contains(err.Error(), "Try:") || strings.Contains(err.Error(), "secret")) {
				t.Fatalf("unsafe error: %v", err)
			}
		})
	}
}

func TestMessageValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		m    Message
	}{
		{"missing text", Message{From: "app@example.com", To: "you@example.com", Subject: "Hi", HTML: "<p>Hi</p>"}},
		{"subject injection", Message{From: "app@example.com", To: "you@example.com", Subject: "Hi\r\nBcc: attacker@example.com", Text: "Hi"}},
		{"address injection", Message{From: "app@example.com", To: "you@example.com\nBcc: attacker@example.com", Subject: "Hi", Text: "Hi"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := (&Capture{}).Send(t.Context(), tc.m); err == nil {
				t.Fatal("invalid message accepted")
			}
		})
	}
}

func TestConfigSecretFile(t *testing.T) {
	t.Parallel()
	var cfg Config
	env := map[string]string{"SMTP_URL": "smtps://user@example.com:465", "SMTP_PASSWORD_FILE": "/secrets/smtp", "SMTP_TIMEOUT": "3s"}
	err := config.LoadFrom(&cfg, func(k string) (string, bool) { v, ok := env[k]; return v, ok }, func(path string) ([]byte, error) {
		if path != "/secrets/smtp" {
			t.Fatal(path)
		}
		return []byte("secret\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Password != "secret" || cfg.Timeout != 3*time.Second {
		t.Fatal("secret file or timeout not loaded")
	}
}

func TestLogOmitsMessageContent(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	err := (Log{Logger: logger}).Send(t.Context(), Message{From: "app@example.com", To: "private@example.com", Subject: "private subject", Text: "secret reset token"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "private") || strings.Contains(buf.String(), "secret") {
		t.Fatalf("message content was logged: %s", buf.String())
	}
}
