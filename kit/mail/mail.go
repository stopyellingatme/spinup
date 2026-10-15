package mail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"sync"
	"time"
)

// Message is an email with a required plain-text part and optional HTML.
type Message struct {
	// From is the sender address.
	From string `json:"from"`
	// To is the recipient address.
	To string `json:"to"`
	// Subject is a single line of text.
	Subject string `json:"subject"`
	// Text is the plain-text body.
	Text string `json:"text"`
	// HTML is the optional HTML alternative.
	HTML string `json:"html,omitempty"`
}

// Mailer delivers an email. Implementations must support concurrent calls.
type Mailer interface {
	Send(ctx context.Context, m Message) error
}

// Config contains SMTP settings. Load it with kit config.Load for _FILE support.
type Config struct {
	// URL selects smtp, smtp+starttls, or smtps. An empty URL selects Log.
	URL string `env:"SMTP_URL"`
	// Password comes from SMTP_PASSWORD or SMTP_PASSWORD_FILE, never the URL.
	Password string `env:"SMTP_PASSWORD"`
	// Timeout limits each send, including connection and SMTP commands.
	Timeout time.Duration `env:"SMTP_TIMEOUT" default:"10s"`
}

func validate(m Message) error {
	if strings.ContainsAny(m.Subject, "\r\n") || strings.TrimSpace(m.Subject) == "" || strings.TrimSpace(m.Text) == "" {
		return errors.New("mail: cannot send: a single-line subject and a plain-text body are required\nTry: provide a subject and a txt template")
	}
	for _, addr := range []string{m.From, m.To} {
		if strings.ContainsAny(addr, "\r\n") {
			return errors.New("mail: cannot send: an address contains a line break\nTry: provide one valid sender and recipient")
		}
		if _, err := mail.ParseAddress(addr); err != nil {
			return errors.New("mail: cannot send: a sender or recipient address is invalid\nTry: provide one valid sender and recipient")
		}
	}
	return nil
}

// Log records delivery metadata without recording email bodies or reset tokens.
// A nil Logger uses slog.Default.
type Log struct {
	// Logger receives delivery events.
	Logger *slog.Logger
}

// Send validates m and logs a delivery event. It does not contact SMTP.
func (l Log) Send(ctx context.Context, m Message) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("mail: cannot log delivery: %w\nTry: retry with an active context", err)
	}
	if err := validate(m); err != nil {
		return err
	}
	logger := l.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.InfoContext(ctx, "mail: delivery logged", "transport", "log")
	return nil
}

// Capture stores messages for tests. Its zero value is ready for use.
type Capture struct {
	mu       sync.Mutex
	messages []Message
}

// Send stores a copy of m.
func (c *Capture) Send(ctx context.Context, m Message) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("mail: cannot capture delivery: %w\nTry: retry with an active context", err)
	}
	if err := validate(m); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = append(c.messages, m)
	return nil
}

// Messages returns a copy of the captured messages, in delivery order.
func (c *Capture) Messages() []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Message(nil), c.messages...)
}
