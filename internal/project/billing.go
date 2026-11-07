package project

import (
	"errors"
	"fmt"
	"go/format"
	"regexp"
	"slices"
	"strings"
)

// Paths of the files that spinup g billing writes (ADR 0032 section 16).
const (
	BillingFilePath   = "api/internal/billing/billing.gen.go"
	BillingTSFilePath = "web/src/lib/billing.gen.ts"
)

// DefaultBillingPlan is the default plan of the [billing] table (ADR 0032 section 3.3).
const DefaultBillingPlan = "free"

// BillingPlan is one [[billing.plans]] table of spinup.toml.
type BillingPlan struct {
	Name        string   `toml:"name"`
	Prices      []string `toml:"prices"`
	Features    []string `toml:"features"`
	APIKeysPlan string   `toml:"apikeys_plan"`
	Role        string   `toml:"role"`
	Meter       string   `toml:"meter"`
}

// BillingDecl is the [billing] table of spinup.toml. The plans are in rank
// order, lowest first.
type BillingDecl struct {
	DefaultPlan         string        `toml:"default_plan"`
	TrialDays           int           `toml:"trial_days"`
	AutomaticTax        bool          `toml:"automatic_tax"`
	AllowPromotionCodes bool          `toml:"allow_promotion_codes"`
	Plans               []BillingPlan `toml:"plans"`
}

// BillingConfig returns the [billing] table with the defaults of ADR 0032
// section 3.3 applied. It does not change c.
func (c *Config) BillingConfig() BillingDecl {
	d := c.Billing
	if d.DefaultPlan == "" {
		d.DefaultPlan = DefaultBillingPlan
	}
	plans := make([]BillingPlan, 0, max(1, len(d.Plans)))
	for _, plan := range d.Plans {
		plan.Prices = slices.Clone(plan.Prices)
		plan.Features = slices.Clone(plan.Features)
		plans = append(plans, plan)
	}
	if len(plans) == 0 {
		plans = append(plans, BillingPlan{Name: d.DefaultPlan})
	}
	d.Plans = plans
	return d
}

// Features returns every feature of every plan, sorted and without repeats.
func (d BillingDecl) Features() []string {
	var all []string
	for _, plan := range d.Plans {
		all = append(all, plan.Features...)
	}
	slices.Sort(all)
	return slices.Compact(all)
}

var (
	billingNamePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	billingPricePattern  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,200}$`)
	billingMeterPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,99}$`)
	billingFieldsAllowed = "default_plan, trial_days, automatic_tax, allow_promotion_codes, plans"
	billingPlanAllowed   = "name, prices, features, apikeys_plan, role, meter"
)

const billingHint = "\nTry: correct [billing] in spinup.toml and run spinup g billing."

// BillingContext is what the [billing] rules of ADR 0032 section 3.2 need
// from the rest of spinup.toml.
type BillingContext struct {
	// APIKeys is true when the app has the apikeys module.
	APIKeys bool
	// APIKeyPlans are the declared [apikeys] plans, after their defaults.
	APIKeyPlans []string
	// Roles are the declared [auth] roles, after their defaults.
	Roles AuthRoles
}

// BillingContext returns the parts of c that the [billing] rules check against.
func (c *Config) BillingContext() BillingContext {
	return BillingContext{
		APIKeys:     slices.Contains(c.Modules, "apikeys"),
		APIKeyPlans: c.APIKeysConfig().PlanNames(),
		Roles:       c.AuthRoles(),
	}
}

// Validate checks the rules of ADR 0032 section 3.2. Call it on the result of
// BillingConfig, so that the defaults are applied.
func (d BillingDecl) Validate(bc BillingContext) error {
	var problems []error
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf("billing: "+format+billingHint, args...))
	}
	if len(d.Plans) < 1 || len(d.Plans) > 16 {
		fail("the plan count %d must be 1 to 16", len(d.Plans))
	}
	if d.TrialDays < 0 || d.TrialDays > 730 {
		fail("trial_days %d must be 0 to 730", d.TrialDays)
	}
	names := map[string]bool{}
	prices := map[string]string{}
	foundDefault := false
	for i, plan := range d.Plans {
		label := fmt.Sprintf("plan %q", plan.Name)
		if !billingNamePattern.MatchString(plan.Name) {
			fail("plan %d: the name %q does not match %s", i+1, plan.Name, billingNamePattern.String())
		}
		if names[plan.Name] {
			fail("%s is declared more than once", label)
		}
		names[plan.Name] = true
		isDefault := plan.Name == d.DefaultPlan
		foundDefault = foundDefault || isDefault
		switch {
		case isDefault && len(plan.Prices) > 0:
			fail("%s is the default plan, so it must have no prices", label)
		case !isDefault && (len(plan.Prices) < 1 || len(plan.Prices) > 8):
			fail("%s must have 1 to 8 prices (it has %d)", label, len(plan.Prices))
		}
		for _, price := range plan.Prices {
			if !billingPricePattern.MatchString(price) {
				fail("%s: the price lookup key %q does not match %s", label, price, billingPricePattern.String())
			} else if other, ok := prices[price]; ok {
				fail("%s: the price lookup key %q is also on plan %q", label, price, other)
			}
			prices[price] = plan.Name
		}
		if len(plan.Features) > 32 {
			fail("%s has %d features, and the limit is 32", label, len(plan.Features))
		}
		seen := map[string]bool{}
		for _, feature := range plan.Features {
			if !billingNamePattern.MatchString(feature) {
				fail("%s: the feature %q does not match %s", label, feature, billingNamePattern.String())
			}
			if seen[feature] {
				fail("%s lists the feature %q more than once", label, feature)
			}
			seen[feature] = true
		}
		if plan.APIKeysPlan != "" {
			if !bc.APIKeys {
				fail("%s sets apikeys_plan, and the app does not have the apikeys module. Run spinup module add apikeys, or remove apikeys_plan", label)
			} else if !slices.Contains(bc.APIKeyPlans, plan.APIKeysPlan) {
				fail("%s: apikeys_plan %q is not an [apikeys] plan (declared: %s)", label, plan.APIKeysPlan, strings.Join(bc.APIKeyPlans, ", "))
			}
		}
		if plan.Role != "" {
			switch {
			case isDefault:
				fail("%s is the default plan, so it must have no role", label)
			case !slices.Contains(bc.Roles.Names, plan.Role):
				fail("%s: the role %q is not an [auth] role (declared: %s)", label, plan.Role, strings.Join(bc.Roles.Names, ", "))
			case plan.Role == bc.Roles.Admin:
				fail("%s: the role %q is the admin role, and a plan cannot grant it", label, plan.Role)
			case plan.Role == bc.Roles.Default:
				fail("%s: the role %q is the default role, and a plan cannot grant it", label, plan.Role)
			}
		}
		if plan.Meter != "" {
			switch {
			case isDefault:
				fail("%s is the default plan, so it must have no meter", label)
			case !billingMeterPattern.MatchString(plan.Meter):
				fail("%s: the meter %q does not match %s", label, plan.Meter, billingMeterPattern.String())
			case !bc.APIKeys:
				fail("%s sets meter, and the app does not have the apikeys module. Run spinup module add apikeys, or remove meter", label)
			}
		}
	}
	if !foundDefault {
		declared := make([]string, 0, len(d.Plans))
		for _, plan := range d.Plans {
			declared = append(declared, plan.Name)
		}
		fail("default_plan %q is not a declared plan (declared: %s)", d.DefaultPlan, strings.Join(declared, ", "))
	}
	return errors.Join(problems...)
}

// RenderBillingFile returns the formatted Go source of api/internal/billing/billing.gen.go.
// d must be the result of BillingConfig, and it must be valid.
func RenderBillingFile(d BillingDecl) []byte {
	var out strings.Builder
	out.WriteString("// Code generated by spinup. DO NOT EDIT.\n\npackage billing\n\nimport kitbilling \"github.com/stopyellingatme/spinup/kit/x/billing\"\n\n")
	out.WriteString("// The plan names of [billing] in spinup.toml, lowest rank first.\nconst (\n")
	for _, plan := range d.Plans {
		fmt.Fprintf(&out, "Plan%s = %q\n", RoleConst(plan.Name), plan.Name)
	}
	out.WriteString(")\n\n")
	if features := d.Features(); len(features) > 0 {
		out.WriteString("// The feature names of [billing] in spinup.toml. Pass them to Require and Has.\nconst (\n")
		for _, feature := range features {
			fmt.Fprintf(&out, "Feature%s = %q\n", RoleConst(feature), feature)
		}
		out.WriteString(")\n\n")
	}
	out.WriteString("// Config is the [billing] table of spinup.toml. Change the table and run spinup g billing.\n")
	fmt.Fprintf(&out, "var Config = kitbilling.Config{\nDefaultPlan: %q,\n", d.DefaultPlan)
	if d.TrialDays != 0 {
		fmt.Fprintf(&out, "TrialDays: %d,\n", d.TrialDays)
	}
	if d.AutomaticTax {
		out.WriteString("AutomaticTax: true,\n")
	}
	if d.AllowPromotionCodes {
		out.WriteString("AllowPromotionCodes: true,\n")
	}
	out.WriteString("Plans: []kitbilling.Plan{\n")
	for _, plan := range d.Plans {
		fmt.Fprintf(&out, "{Name: %q", plan.Name)
		if len(plan.Prices) > 0 {
			fmt.Fprintf(&out, ", Prices: %s", goStrings(plan.Prices))
		}
		if len(plan.Features) > 0 {
			fmt.Fprintf(&out, ", Features: %s", goStrings(plan.Features))
		}
		for _, field := range [][2]string{{"APIKeysPlan", plan.APIKeysPlan}, {"Role", plan.Role}, {"Meter", plan.Meter}} {
			if field[1] != "" {
				fmt.Fprintf(&out, ", %s: %q", field[0], field[1])
			}
		}
		out.WriteString("},\n")
	}
	out.WriteString("},\n}\n")
	data, err := format.Source([]byte(out.String()))
	if err != nil {
		panic(fmt.Sprintf("render billing file: %v\nTry: validate [billing] before rendering.", err))
	}
	return data
}

func goStrings(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = fmt.Sprintf("%q", value)
	}
	return "[]string{" + strings.Join(quoted, ", ") + "}"
}

func tsUnion(values []string) string {
	if len(values) == 0 {
		return "never"
	}
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = "'" + value + "'"
	}
	return strings.Join(quoted, " | ")
}

// RenderBillingTSFile returns the TypeScript source of web/src/lib/billing.gen.ts.
func RenderBillingTSFile(d BillingDecl) []byte {
	names := make([]string, len(d.Plans))
	for i, plan := range d.Plans {
		names[i] = plan.Name
	}
	features := d.Features()
	var out strings.Builder
	out.WriteString("// Code generated by spinup. DO NOT EDIT.\n\n")
	out.WriteString("/** The plan names of [billing] in spinup.toml, lowest rank first. */\n")
	fmt.Fprintf(&out, "export const plans = [%s] as const;\n\n", strings.Join(quoteAll(names, "'"), ", "))
	fmt.Fprintf(&out, "export type Plan = %s;\n\n", tsUnion(names))
	fmt.Fprintf(&out, "/** The feature names of [billing] in spinup.toml. */\nexport type Feature = %s;\n\n", tsUnion(features))
	fmt.Fprintf(&out, "/** The plan of a user with no live subscription. */\nexport const defaultPlan: Plan = '%s';\n\n", d.DefaultPlan)
	out.WriteString("/** Reports whether the billing summary of a user includes the feature. */\n")
	out.WriteString("export function hasFeature(billing: { features: readonly string[] } | null | undefined, feature: Feature): boolean {\n\treturn billing?.features.includes(feature) ?? false;\n}\n")
	return []byte(out.String())
}

func quoteAll(values []string, quote string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = quote + value + quote
	}
	return out
}

// checkBillingFields rejects a key in [billing] or in a plan table that ADR
// 0032 section 3 does not define. It reads the raw TOML in sorted order.
func checkBillingFields(raw any) error {
	if raw == nil {
		return nil
	}
	table, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("billing: [billing] must be a table%s", billingHint)
	}
	for _, field := range sortedKeys(table) {
		switch field {
		case "default_plan", "trial_days", "automatic_tax", "allow_promotion_codes":
		case "plans":
			plans, ok := table[field].([]any)
			if !ok {
				return fmt.Errorf("billing: plans must be an array of tables ([[billing.plans]])%s", billingHint)
			}
			for i, entry := range plans {
				plan, ok := entry.(map[string]any)
				if !ok {
					return fmt.Errorf("billing: plan %d must be a table ([[billing.plans]])%s", i+1, billingHint)
				}
				for _, key := range sortedKeys(plan) {
					switch key {
					case "name", "prices", "features", "apikeys_plan", "role", "meter":
					default:
						return fmt.Errorf("billing: plan %d: unknown key %q (allowed: %s)%s", i+1, key, billingPlanAllowed, billingHint)
					}
				}
			}
		default:
			return fmt.Errorf("billing: unknown key %q (allowed: %s)%s", field, billingFieldsAllowed, billingHint)
		}
	}
	return nil
}
