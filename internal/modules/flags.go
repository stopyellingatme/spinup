package modules

// FlagsNextSteps returns the manual wiring steps for an existing app with flags.
func FlagsNextSteps() string {
	return `Next: declare a flag in spinup.toml, for example:
 [flags.new_checkout]
 description = "The new checkout flow"
 public = true
Next: run spinup g flags to write api/internal/flags/flags.gen.go and web/src/lib/flags.gen.ts.
Next: in api/cmd/api/main.go, import:
 appflags "<module>/api/internal/flags"
 "github.com/stopyellingatme/spinup/kit/x/flags"
Next: before app.New, read FLAGS_REFRESH, build flags.New(flags.WithDeclared(appflags.Set)),
 flags.NewStore(pool, flagsProvider, roles.Set), and call flagStore.Start(ctx, refresh). See docs/build/flags.md.
Next: add these options to app.New(...), after app.WithMiddleware(authn.Middleware()):
 app.WithMiddleware(flagsProvider.Middleware()),
 app.WithRoutes(flagStore.Routes(authn)),
 app.WithCommand("flags", flagStore.Command()),
 app.WithReadiness("flags", flagStore.Ready),
 app.WithShutdown(flagStore.Close),
Next: in web/src/hooks.server.ts, load GET /api/flags next to the session and set locals.flags.
 In web/src/app.d.ts add flags: Record<string, boolean> to Locals. In web/src/routes/+layout.server.ts return flags.
 In web/src/routes/+layout.svelte pass the Feature flags link to UserMenu. See docs/build/flags.md.
Next: run spinup db migrate up before starting the new binary.
Next: run spinup doctor and inspect the flags-drift and flags-wired checks.
`
}
