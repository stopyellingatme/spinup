package templates_test

import (
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/testutil"
)

// TestEmbedAuth checks the account pages of the auth pack in embed mode (ADR 0039, sections 10.1
// and 10.3), with and without the mail module and OIDC, and in node mode.
func TestEmbedAuth(t *testing.T) {
	t.Parallel()
	pages := []string{
		"web/src/lib/auth-form.ts",
		"web/src/routes/login/+page.ts",
		"web/src/routes/login/+page.svelte",
		"web/src/routes/login/schema.ts",
		"web/src/routes/register/+page.ts",
		"web/src/routes/register/+page.svelte",
		"web/src/routes/account/+page.ts",
		"web/src/routes/account/+page.svelte",
	}
	nodeFiles := []string{
		"web/src/lib/server/auth.ts",
		"web/src/lib/server/auth.test.ts",
		"web/src/routes/login/+page.server.ts",
		"web/src/routes/register/+page.server.ts",
		"web/src/routes/account/+page.server.ts",
		"web/src/routes/logout/+page.server.ts",
	}
	resetPages := []string{
		"web/src/routes/reset/+page.ts",
		"web/src/routes/reset/+page.svelte",
		"web/src/routes/reset/schema.ts",
		"web/src/routes/reset/confirm/+page.ts",
		"web/src/routes/reset/confirm/+page.svelte",
		"web/src/routes/reset/confirm/schema.ts",
		"web/src/lib/reset-token.ts",
		"web/src/lib/reset-token.test.ts",
	}
	resetNodeFiles := []string{
		"web/src/routes/reset/+page.server.ts",
		"web/src/routes/reset/[token]/+page.server.ts",
		"web/src/routes/reset/[token]/+page.svelte",
	}
	const oidcMarker = "spinup:oidc-pending"

	for _, tc := range []struct {
		name    string
		modules string
		oidc    bool
	}{
		{name: "plain", modules: "ci,auth"},
		{name: "mail", modules: "ci,auth,mail"},
		{name: "oidc", modules: "ci,auth", oidc: true},
		{name: "mail and oidc", modules: "ci,auth,mail", oidc: true},
	} {
		mail := strings.Contains(tc.modules, "mail")
		t.Run("embed "+tc.name, func(t *testing.T) {
			t.Parallel()
			packs := []string{"base", "api", "web", "auth"}
			if mail {
				packs = append(packs, "mail")
			}
			tree := testutil.RenderPack(t, packs, map[string]any{
				"app_name": "todo", "module": "example.com/todo", "modules": tc.modules,
				"frontend_mode": "embed", "auth_oidc": tc.oidc,
			})
			for _, name := range pages {
				if _, err := fs.Stat(tree, name); err != nil {
					t.Errorf("missing %s: %v", name, err)
				}
			}
			for _, name := range nodeFiles {
				if _, err := fs.Stat(tree, name); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("unexpected %s: %v", name, err)
				}
			}
			// No server file and no logout route stays in the tree, also for the reset pages.
			err := fs.WalkDir(tree, "web", func(path string, entry fs.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return err
				}
				if strings.HasSuffix(path, "+page.server.ts") || strings.HasSuffix(path, "+layout.server.ts") ||
					strings.Contains(path, "/lib/server/") || strings.Contains(path, "/routes/logout/") {
					t.Errorf("unexpected node file %s in an embed render", path)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}

			for _, name := range append(append([]string{}, resetPages...), resetNodeFiles...) {
				_, err := fs.Stat(tree, name)
				wantPresent := mail && !slices.Contains(resetNodeFiles, name)
				if (err == nil) != wantPresent {
					t.Errorf("%s: present=%v, want %v (%v)", name, err == nil, wantPresent, err)
				}
			}
			if mail {
				testResetEmbed(t, tree)
			}
			// The root layout imports the reset token module, which takes the token before the router
			// reads the first address. Without mail it has no reset token module to import.
			if layout := embedFile(t, tree, "web/src/routes/+layout.ts"); strings.Contains(layout, "import '#lib/reset-token.js';") != mail {
				t.Errorf("+layout.ts imports the reset token module: %v, want %v", !mail, mail)
			}
			// The root layout component ends the visit of the reset page after each navigation, so
			// a held token lives for one visit (ADR 0039, section 10.10).
			if layout := embedFile(t, tree, "web/src/routes/+layout.svelte"); strings.Contains(layout, "afterNavigate(() => followResetVisit());") != mail {
				t.Errorf("+layout.svelte ends the visit of the reset page: %v, want %v", !mail, mail)
			}
			// The reset link of embed mode holds the token in the fragment, so no render has a
			// header middleware or a token route.
			for _, name := range []string{"api/cmd/api/main.go", "api/cmd/api/reset.go"} {
				data, err := fs.ReadFile(tree, name)
				if errors.Is(err, fs.ErrNotExist) && name == "api/cmd/api/reset.go" && !mail {
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), "resetPageHeaders") {
					t.Errorf("%s mentions resetPageHeaders", name)
				}
			}
			if mail {
				reset := embedFile(t, tree, "api/cmd/api/reset.go")
				for _, want := range []string{`"/reset/confirm"), auth.WithResetFragment(), auth.WithResetTemplates(`} {
					if !strings.Contains(reset, want) {
						t.Errorf("embed reset.go lacks %q", want)
					}
				}
			} else if _, err := fs.Stat(tree, "api/cmd/api/reset.go"); err == nil {
				t.Errorf("reset.go exists without the mail module")
			}
			walkErr := fs.WalkDir(tree, ".", func(path string, entry fs.DirEntry, err error) error {
				if err == nil && strings.Contains(path, "[token]") {
					t.Errorf("embed render has the token route %s", path)
				}
				return err
			})
			if walkErr != nil {
				t.Fatal(walkErr)
			}

			login := embedFile(t, tree, "web/src/routes/login/+page.svelte")
			for _, want := range []string{
				"authForm(loginSchema, '/api/auth/login'",
				`data-testid="form-error"`,
				"Too many sign-in attempts. Try again later.",
				"maxlength={512}",
				`<Label for="email">Email</Label>`,
				`<Label for="password">Password</Label>`,
				">Sign in</Button>",
			} {
				if !strings.Contains(login, want) {
					t.Errorf("login page lacks %q", want)
				}
			}
			for _, tt := range []struct {
				name string
				want string
				on   bool
			}{
				{"reset link", `<a href="/reset"`, mail},
				{"provider buttons", `<OidcButtons next={data.redirectTo} />`, tc.oidc},
				{"provider import", "import OidcButtons from '#lib/components/OidcButtons.svelte';", tc.oidc},
				{"pending marker", oidcMarker, tc.oidc},
			} {
				if got := strings.Contains(login, tt.want); got != tt.on {
					t.Errorf("login page %s: present=%v, want %v (%q)", tt.name, got, tt.on, tt.want)
				}
			}
			if strings.Contains(login, "[[") || strings.Contains(login, "\n\n\n") {
				t.Errorf("login page keeps template text or blank lines:\n%s", login)
			}

			register := embedFile(t, tree, "web/src/routes/register/+page.svelte")
			for _, want := range []string{"authForm(registerSchema, '/api/auth/register'", `data-testid="form-error"`, "minlength={12}", "maxlength={512}", ">Create account</Button>"} {
				if !strings.Contains(register, want) {
					t.Errorf("register page lacks %q", want)
				}
			}
			account := embedFile(t, tree, "web/src/routes/account/+page.svelte")
			for _, want := range []string{"signOut()", `data-testid="account-email"`, ">Sign out</Button>"} {
				if !strings.Contains(account, want) {
					t.Errorf("account page lacks %q", want)
				}
			}
			if strings.Contains(account, "/logout") {
				t.Errorf("account page posts to the node logout route")
			}
			form := embedFile(t, tree, "web/src/lib/auth-form.ts")
			for _, want := range []string{"SPA: true", "resetForm: false", "announceSessionChange()", "leaveSession(redirectTo())"} {
				if !strings.Contains(form, want) {
					t.Errorf("auth-form.ts lacks %q", want)
				}
			}
			// The shared button component stays as it is: the pages add the marker around it.
			if oidc := tc.oidc; oidc {
				if buttons := embedFile(t, tree, "web/src/lib/components/OidcButtons.svelte"); strings.Contains(buttons, oidcMarker) {
					t.Errorf("OidcButtons.svelte sets the pending marker")
				}
			}
		})
	}

	// Node mode keeps its form actions and gets none of the browser twins.
	t.Run("node", func(t *testing.T) {
		t.Parallel()
		tree := testutil.RenderPack(t, []string{"base", "api", "web", "auth"}, map[string]any{
			"app_name": "todo", "module": "example.com/todo", "modules": "ci,auth",
		})
		for _, name := range nodeFiles {
			if _, err := fs.Stat(tree, name); err != nil {
				t.Errorf("missing %s: %v", name, err)
			}
		}
		for _, name := range []string{
			"web/src/lib/auth-form.ts",
			"web/src/routes/login/+page.ts",
			"web/src/routes/login/schema.ts",
			"web/src/routes/register/+page.ts",
			"web/src/routes/account/+page.ts",
		} {
			if _, err := fs.Stat(tree, name); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("unexpected %s: %v", name, err)
			}
		}
		node := testutil.RenderPack(t, []string{"base", "api", "web", "auth", "mail"}, map[string]any{
			"app_name": "todo", "module": "example.com/todo", "modules": "ci,auth,mail",
		})
		for _, name := range resetNodeFiles {
			if _, err := fs.Stat(node, name); err != nil {
				t.Errorf("node mode lacks %s: %v", name, err)
			}
		}
		for _, name := range resetPages {
			if _, err := fs.Stat(node, name); name != "web/src/routes/reset/+page.svelte" && !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("node mode has the embed file %s: %v", name, err)
			}
		}
		for _, name := range []string{"api/cmd/api/reset.go", "api/cmd/api/main.go"} {
			data := embedFile(t, node, name)
			for _, bad := range []string{"resetPageHeaders", "WithResetFragment", "/reset/confirm"} {
				if strings.Contains(data, bad) {
					t.Errorf("node %s mentions %s", name, bad)
				}
			}
		}
		if reset := embedFile(t, node, "api/cmd/api/reset.go"); !strings.Contains(reset, `"/reset"), auth.WithResetTemplates(`) {
			t.Errorf("node reset.go lost the path link")
		}
		if page := embedFile(t, node, "web/src/routes/reset/[token]/+page.svelte"); !strings.Contains(page, "$app/forms") {
			t.Errorf("node reset page does not use the form action")
		}
		if login := embedFile(t, tree, "web/src/routes/login/+page.svelte"); !strings.Contains(login, "$app/forms") {
			t.Errorf("node login page does not use the form action")
		}
	})
}

// testResetEmbed checks the two reset pages of an embed render.
func testResetEmbed(t *testing.T, tree fs.FS) {
	t.Helper()
	request := embedFile(t, tree, "web/src/routes/reset/+page.svelte")
	for _, want := range []string{
		"/api/auth/reset/request",
		"If an account exists for this email address, you will receive a reset link.",
		`<meta name="referrer" content="no-referrer" />`,
		"<h1 class=\"text-2xl font-semibold\">Reset your password</h1>",
		"Send a reset link",
		"Return to sign in",
		"SPA: true",
	} {
		if !strings.Contains(request, want) {
			t.Errorf("reset page lacks %q", want)
		}
	}
	confirm := embedFile(t, tree, "web/src/routes/reset/confirm/+page.svelte")
	for _, want := range []string{
		"/api/auth/reset/confirm",
		"const token = $derived(data.token);",
		"token === null",
		"The reset link is not valid. Request a new link.",
		`<meta name="referrer" content="no-referrer" />`,
		"Choose a new password",
		"New password",
		"Save the new password",
		"Request a new link",
		"announceSessionChange();",
		"leaveSession('/login');",
		"minlength={12}",
		"maxlength={512}",
	} {
		if !strings.Contains(confirm, want) {
			t.Errorf("reset confirm page lacks %q", want)
		}
	}
	// The token never goes to storage, a log line, or a request address.
	for _, name := range []string{"web/src/routes/reset/confirm/+page.svelte", "web/src/routes/reset/confirm/+page.ts", "web/src/lib/reset-token.ts"} {
		data := embedFile(t, tree, name)
		for _, bad := range []string{"console.", "localStorage", "sessionStorage", "document.cookie", "observedFetch", "?token", "${token}", "params.token"} {
			if strings.Contains(data, bad) {
				t.Errorf("%s contains %q", name, bad)
			}
		}
	}
	helper := embedFile(t, tree, "web/src/lib/reset-token.ts")
	for _, want := range []string{
		"history.replaceState(history.state, '', location.pathname + location.search)",
		"if (location.hash === '') return held;",
		"window.location?.pathname === CONFIRM) takeResetToken();",
		"if (location.pathname !== CONFIRM) {\n\t\theld = null;",
		"window.addEventListener('popstate', onAddress, { capture: true });",
		"window.addEventListener('hashchange', onAddress);",
		"void invalidate(RESET_TOKEN_DEP);",
	} {
		if !strings.Contains(helper, want) {
			t.Errorf("reset-token.ts lacks %q", want)
		}
	}
	// The universal load takes the token: it runs also when the root layout hides the page.
	if load := embedFile(t, tree, "web/src/routes/reset/confirm/+page.ts"); !strings.Contains(load, "depends(RESET_TOKEN_DEP);\n\treturn { token: takeResetToken() };") {
		t.Errorf("the reset confirm load does not take the token:\n%s", load)
	}
	if strings.Contains(confirm, "takeResetToken") {
		t.Errorf("the reset confirm page takes the token itself: only the load takes it")
	}
}
