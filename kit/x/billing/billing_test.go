package billing

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/x/billing/stripe"
)

func TestConfigValidate(t *testing.T) {
	if err := testConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	many := func(n int, prefix string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = prefix + string(rune('a'+i%26)) + strings.Repeat("x", i/26)
		}
		return out
	}
	cases := map[string]func(*Config){
		"no plans":            func(c *Config) { c.Plans = nil },
		"17 plans":            func(c *Config) { c.Plans = append(c.Plans, make([]Plan, 14)...) },
		"bad name":            func(c *Config) { c.Plans[1].Name = "Pro" },
		"repeated name":       func(c *Config) { c.Plans[2].Name = "pro" },
		"default with prices": func(c *Config) { c.Plans[0].Prices = []string{"free_monthly"} },
		"default with role":   func(c *Config) { c.Plans[0].Role = "pro" },
		"default with meter":  func(c *Config) { c.Plans[0].Meter = "api_calls" },
		"no prices":           func(c *Config) { c.Plans[1].Prices = nil },
		"9 prices":            func(c *Config) { c.Plans[1].Prices = many(9, "p") },
		"bad lookup key":      func(c *Config) { c.Plans[1].Prices[0] = "pro monthly" },
		"shared lookup key":   func(c *Config) { c.Plans[2].Prices = []string{"pro_monthly"} },
		"bad feature":         func(c *Config) { c.Plans[1].Features = []string{"Export"} },
		"repeated feature":    func(c *Config) { c.Plans[1].Features = []string{"a", "a"} },
		"33 features":         func(c *Config) { c.Plans[1].Features = many(33, "f") },
		"bad meter":           func(c *Config) { c.Plans[1].Meter = "API" },
		"bad role":            func(c *Config) { c.Plans[1].Role = "Pro" },
		"bad apikeys plan":    func(c *Config) { c.Plans[1].APIKeysPlan = "Pro" },
		"undeclared default":  func(c *Config) { c.DefaultPlan = "basic" },
		"negative trial days": func(c *Config) { c.TrialDays = -1 },
		"too many trial days": func(c *Config) { c.TrialDays = 731 },
	}
	for name, change := range cases {
		cfg := testConfig()
		change(&cfg)
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "Try: correct [billing]") {
			t.Errorf("%s: %v", name, err)
		}
	}
	cfg := testConfig()
	if p, ok := cfg.Plan("team"); !ok || p.Role != "team" {
		t.Fatalf("Plan(team) = %+v, %t", p, ok)
	}
	if _, ok := cfg.Plan("gold"); ok {
		t.Fatal("Plan(gold) found")
	}
}

func TestNew(t *testing.T) {
	pool := &pgxpool.Pool{}
	client, err := stripe.New("sk_test_x")
	if err != nil {
		t.Fatal(err)
	}
	ok := []Option{WithPublicOrigin("https://app.example.com/"), WithRoles(testRoles())}
	s, err := New(pool, testConfig(), client, ok...)
	if err != nil {
		t.Fatal(err)
	}
	if s.origin != "https://app.example.com" {
		t.Fatalf("origin = %q", s.origin)
	}
	if _, isMeter := s.reporter.(MeterReporter); !isMeter {
		t.Fatalf("default reporter = %T", s.reporter)
	}
	roles := testRoles()
	cases := map[string]func() (*Service, error){
		"nil pool":   func() (*Service, error) { return New(nil, testConfig(), client, ok...) },
		"bad config": func() (*Service, error) { return New(pool, Config{}, client, ok...) },
		"no origin":  func() (*Service, error) { return New(pool, testConfig(), client, WithRoles(roles)) },
		"origin path": func() (*Service, error) {
			return New(pool, testConfig(), client, WithRoles(roles), WithPublicOrigin("https://a.test/app"))
		},
		"origin scheme": func() (*Service, error) {
			return New(pool, testConfig(), client, WithRoles(roles), WithPublicOrigin("ftp://a.test"))
		},
		"role, no roles": func() (*Service, error) { return New(pool, testConfig(), client, WithPublicOrigin("https://a.test")) },
		"undeclared role": func() (*Service, error) {
			return New(pool, testConfig(), client, ok[0], WithRoles(auth.DefaultRoles()))
		},
		"admin role": func() (*Service, error) {
			cfg := testConfig()
			cfg.Plans[1].Role = "manager"
			return New(pool, cfg, client, ok...)
		},
		"default role": func() (*Service, error) {
			cfg := testConfig()
			cfg.Plans[1].Role = "staff"
			return New(pool, cfg, client, ok...)
		},
	}
	for name, build := range cases {
		if _, err := build(); err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// No plan with a role needs no roles.
	if _, err := New(pool, Config{DefaultPlan: "free", Plans: []Plan{{Name: "free"}}}, client, WithPublicOrigin("http://localhost:5173")); err != nil {
		t.Fatal(err)
	}
	// Webhook secrets split on commas and drop blank parts.
	s, err = New(pool, testConfig(), client, append(ok, WithWebhookSecrets("a, b,", " ", "c"))...)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(s.secrets, "|") != "a|b|c" {
		t.Fatalf("secrets = %q", s.secrets)
	}
}
