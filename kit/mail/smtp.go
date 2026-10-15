package mail

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	gomail "github.com/wneessen/go-mail"
)

type smtpMailer struct {
	host    string
	opts    []gomail.Option
	timeout time.Duration
}

// New returns an SMTP mailer, or a Log mailer when cfg.URL is empty.
// smtp allows plaintext for local Mailpit. Authenticated smtp requires STARTTLS.
// smtp+starttls requires STARTTLS. smtps requires implicit TLS without fallback.
func New(cfg Config) (Mailer, error) {
	if cfg.URL == "" {
		return Log{}, nil
	}
	bad := func() (Mailer, error) {
		return nil, errors.New("mail: cannot configure SMTP: the URL or timeout is invalid\nTry: use smtp://host:1025, smtp+starttls://user@host:587, or smtps://user@host:465 and SMTP_PASSWORD")
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Hostname() == "" || u.Path != "" || u.Fragment != "" || u.Opaque != "" {
		return bad()
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.Timeout < 0 {
		return bad()
	}
	if u.User != nil {
		if _, has := u.User.Password(); has {
			return bad()
		}
	}
	port := 25
	policy := gomail.NoTLS
	implicit := false
	switch u.Scheme {
	case "smtp":
		if u.User != nil && u.User.Username() != "" {
			policy = gomail.TLSMandatory
			port = 587
		}
	case "smtp+starttls":
		policy = gomail.TLSMandatory
		port = 587
	case "smtps":
		implicit = true
		port = 465
	default:
		return bad()
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) > 1 || len(query["tls"]) > 1 {
		return bad()
	}
	for key := range query {
		if key != "tls" {
			return bad()
		}
	}
	switch query.Get("tls") {
	case "":
	case "starttls":
		if implicit {
			return bad()
		}
		policy = gomail.TLSMandatory
	case "none":
		if u.Scheme != "smtp" || u.User != nil && u.User.Username() != "" {
			return bad()
		}
		policy = gomail.NoTLS
	default:
		return bad()
	}
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return bad()
		}
	}
	opts := []gomail.Option{gomail.WithPort(port), gomail.WithTimeout(cfg.Timeout), gomail.WithTLSPolicy(policy)}
	if implicit {
		opts = append(opts, gomail.WithSSL())
	}
	if u.User != nil && u.User.Username() != "" {
		opts = append(opts, gomail.WithUsername(u.User.Username()), gomail.WithPassword(cfg.Password), gomail.WithSMTPAuth(gomail.SMTPAuthPlain))
	}
	if _, err := gomail.NewClient(u.Hostname(), opts...); err != nil {
		return bad()
	}
	return &smtpMailer{host: u.Hostname(), opts: opts, timeout: cfg.Timeout}, nil
}

func (s *smtpMailer) Send(ctx context.Context, m Message) error {
	if err := validate(m); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	// A client holds connection state. Each send owns its client.
	client, err := gomail.NewClient(s.host, s.opts...)
	if err != nil {
		return errors.New("mail: cannot create SMTP client\nTry: check SMTP_URL and SMTP_TIMEOUT")
	}
	msg := gomail.NewMsg()
	if err := msg.From(m.From); err != nil {
		return errors.New("mail: cannot set sender\nTry: check MAIL_FROM")
	}
	if err := msg.To(m.To); err != nil {
		return errors.New("mail: cannot set recipient\nTry: provide a valid recipient address")
	}
	msg.Subject(m.Subject)
	msg.SetBodyString(gomail.TypeTextPlain, m.Text)
	if m.HTML != "" {
		msg.AddAlternativeString(gomail.TypeTextHTML, m.HTML)
	}
	if err := client.DialAndSendWithContext(ctx, msg); err != nil {
		// SMTP errors can contain credentials or message content. Do not expose them.
		if ctx.Err() != nil {
			return fmt.Errorf("mail: SMTP delivery timed out or was canceled: %w\nTry: retry delivery or check SMTP_TIMEOUT", ctx.Err())
		}
		return errors.New("mail: SMTP delivery failed\nTry: check SMTP_URL, SMTP_PASSWORD, TLS, and SMTP server availability")
	}
	return nil
}
