package project

import (
	"strings"
	"testing"
)

const billingTOML = `modules = ["auth", "jobs", "apikeys", "billing"]

[app]
name = "shop"
module = "example.com/shop"

[auth]
roles = ["owner", "member", "pro"]
admin_role = "owner"
default_role = "member"

[apikeys.plans.free]
per_minute = 10
per_day = 100

[apikeys.plans.pro]
per_minute = 100
per_day = 10000

[billing]
trial_days = 14
automatic_tax = true

[[billing.plans]]
name = "free"
features = ["basic"]
apikeys_plan = "free"

[[billing.plans]]
name = "pro"
prices = ["pro_monthly", "pro_yearly"]
features = ["basic", "export"]
apikeys_plan = "pro"
role = "pro"
meter = "api_calls"
`

func parseBilling(t *testing.T, doc string) *Config {
	t.Helper()
	c, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBillingDefaults(t *testing.T) {
	var c Config
	d := c.BillingConfig()
	if d.DefaultPlan != "free" || len(d.Plans) != 1 || d.Plans[0].Name != "free" {
		t.Fatalf("defaults = %#v", d)
	}
	if err := d.Validate(c.BillingContext()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(RenderBillingFile(d)), "Feature") {
		t.Fatal("default render has feature constants")
	}
	if !strings.Contains(string(RenderBillingTSFile(d)), "export type Feature = never;") {
		t.Fatalf("default TS:\n%s", RenderBillingTSFile(d))
	}
}

func TestBillingParseAndRender(t *testing.T) {
	c := parseBilling(t, billingTOML)
	if _, ok := c.Extra["billing"]; ok {
		t.Fatal("billing kept in Extra")
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	d := c.BillingConfig()
	if got := strings.Join(d.Features(), ","); got != "basic,export" {
		t.Fatalf("features = %s", got)
	}
	goSrc := string(RenderBillingFile(d))
	for _, want := range []string{
		`PlanFree = "free"`, `PlanPro  = "pro"`, `FeatureExport = "export"`, `TrialDays:    14`, `AutomaticTax: true`,
		`{Name: "pro", Prices: []string{"pro_monthly", "pro_yearly"}, Features: []string{"basic", "export"}, APIKeysPlan: "pro", Role: "pro", Meter: "api_calls"}`,
	} {
		if !strings.Contains(goSrc, want) {
			t.Errorf("Go render lacks %q:\n%s", want, goSrc)
		}
	}
	ts := string(RenderBillingTSFile(d))
	for _, want := range []string{"export const plans = ['free', 'pro'] as const;", "export type Feature = 'basic' | 'export';", "export const defaultPlan: Plan = 'free';"} {
		if !strings.Contains(ts, want) {
			t.Errorf("TS render lacks %q:\n%s", want, ts)
		}
	}
	if string(RenderBillingFile(d)) != goSrc {
		t.Fatal("render is not deterministic")
	}
}

func TestBillingValidation(t *testing.T) {
	ctx := BillingContext{APIKeys: true, APIKeyPlans: []string{"free", "pro"}, Roles: AuthRoles{Names: []string{"owner", "member", "pro"}, Admin: "owner", Default: "member"}}
	free := BillingPlan{Name: "free"}
	pro := func(change func(*BillingPlan)) BillingPlan {
		p := BillingPlan{Name: "pro", Prices: []string{"pro_monthly"}}
		change(&p)
		return p
	}
	for name, tc := range map[string]struct {
		d    BillingDecl
		ctx  BillingContext
		want string
	}{
		"no default":        {BillingDecl{DefaultPlan: "basic", Plans: []BillingPlan{free}}, ctx, `default_plan "basic" is not a declared plan`},
		"bad name":          {BillingDecl{DefaultPlan: "free", Plans: []BillingPlan{free, {Name: "Pro", Prices: []string{"x"}}}}, ctx, `the name "Pro" does not match`},
		"repeat name":       {BillingDecl{DefaultPlan: "free", Plans: []BillingPlan{free, free}}, ctx, "declared more than once"},
		"default prices":    {BillingDecl{DefaultPlan: "free", Plans: []BillingPlan{{Name: "free", Prices: []string{"x"}}}}, ctx, "must have no prices"},
		"paid no prices":    {BillingDecl{DefaultPlan: "free", Plans: []BillingPlan{free, pro(func(p *BillingPlan) { p.Prices = nil })}}, ctx, "must have 1 to 8 prices"},
		"bad price":         {BillingDecl{DefaultPlan: "free", Plans: []BillingPlan{free, pro(func(p *BillingPlan) { p.Prices = []string{"a b"} })}}, ctx, "price lookup key"},
		"shared price":      {BillingDecl{DefaultPlan: "free", Plans: []BillingPlan{free, pro(func(*BillingPlan) {}), {Name: "max", Prices: []string{"pro_monthly"}}}}, ctx, `is also on plan "pro"`},
		"bad feature":       {BillingDecl{DefaultPlan: "free", Plans: []BillingPlan{{Name: "free", Features: []string{"Export"}}}}, ctx, `the feature "Export"`},
		"repeat feature":    {BillingDecl{DefaultPlan: "free", Plans: []BillingPlan{{Name: "free", Features: []string{"a", "a"}}}}, ctx, "more than once"},
		"apikeys missing":   {BillingDecl{DefaultPlan: "free", Plans: []BillingPlan{{Name: "free", APIKeysPlan: "free"}}}, BillingContext{Roles: ctx.Roles}, "does not have the apikeys module"},
		"apikeys unknown":   {BillingDecl{DefaultPlan: "free", Plans: []BillingPlan{{Name: "free", APIKeysPlan: "gold"}}}, ctx, `apikeys_plan "gold" is not an [apikeys] plan`},
		"role unknown":      {BillingDecl{DefaultPlan: "free", Plans: []BillingPlan{free, pro(func(p *BillingPlan) { p.Role = "vip" })}}, ctx, `the role "vip" is not an [auth] role`},
		"role admin":        {BillingDecl{DefaultPlan: "free", Plans: []BillingPlan{free, pro(func(p *BillingPlan) { p.Role = "owner" })}}, ctx, "is the admin role"},
		"role default":      {BillingDecl{DefaultPlan: "free", Plans: []BillingPlan{free, pro(func(p *BillingPlan) { p.Role = "member" })}}, ctx, "is the default role"},
		"role on default":   {BillingDecl{DefaultPlan: "free", Plans: []BillingPlan{{Name: "free", Role: "pro"}}}, ctx, "must have no role"},
		"meter on default":  {BillingDecl{DefaultPlan: "free", Plans: []BillingPlan{{Name: "free", Meter: "calls"}}}, ctx, "must have no meter"},
		"meter bad":         {BillingDecl{DefaultPlan: "free", Plans: []BillingPlan{free, pro(func(p *BillingPlan) { p.Meter = "API" })}}, ctx, `the meter "API"`},
		"meter no apikeys":  {BillingDecl{DefaultPlan: "free", Plans: []BillingPlan{free, pro(func(p *BillingPlan) { p.Meter = "calls" })}}, BillingContext{Roles: ctx.Roles}, "remove meter"},
		"trial too long":    {BillingDecl{DefaultPlan: "free", TrialDays: 731, Plans: []BillingPlan{free}}, ctx, "trial_days 731"},
		"too many features": {BillingDecl{DefaultPlan: "free", Plans: []BillingPlan{{Name: "free", Features: manyFeatures(33)}}}, ctx, "the limit is 32"},
	} {
		t.Run(name, func(t *testing.T) {
			err := tc.d.Validate(tc.ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "Try: correct [billing]") {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func manyFeatures(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "f" + strings.Repeat("x", i)
	}
	return out
}

func TestBillingUnknownFields(t *testing.T) {
	head := "[app]\nname = \"shop\"\nmodule = \"example.com/shop\"\n"
	for doc, want := range map[string]string{
		head + "[billing]\ncurrency = \"usd\"\n":                     `unknown key "currency"`,
		head + "[billing.plans.free]\nfeatures = []\n":               "must be an array of tables",
		head + "[[billing.plans]]\nname = \"free\"\nprice = \"x\"\n": `plan 1: unknown key "price"`,
	} {
		if _, err := Parse([]byte(doc)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q) = %v, want %q", doc, err, want)
		}
	}
}
