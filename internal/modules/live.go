package modules

// LiveNextSteps returns the manual wiring steps for an existing app with live data in node mode (ADR 0033).
func LiveNextSteps() string {
	return liveGoSteps + liveNodePageStep + liveTailSteps
}

// LiveNextStepsFor returns the manual wiring steps for the frontend mode of the lock. Embed mode
// refreshes only the data of one resource: the page invalidates its RESOURCE_DEP (ADR 0039,
// sections 10.5 and 10.7). Node mode gets the bytes of LiveNextSteps.
func LiveNextStepsFor(frontendMode any) string {
	if frontendMode == "embed" {
		return liveGoSteps + liveEmbedPageStep + liveTailSteps
	}
	return LiveNextSteps()
}

const liveGoSteps = `Next: run spinup db migrate up. It adds the tables live_feeds and live_outbox.
Next: in api/cmd/api/main.go, import:
 applive "<module>/api/internal/live"
 kitlive "github.com/stopyellingatme/spinup/kit/x/live"
Next: before app.New, read LIVE_MAX_STREAMS and LIVE_MAX_STREAMS_PER_USER, build the hub with
 kitlive.New(pool, ...), and call liveHub.Start(ctx):
 var liveEnv struct {
 	MaxStreams        int ` + "`" + `env:"LIVE_MAX_STREAMS" default:"2000"` + "`" + `
 	MaxStreamsPerUser int ` + "`" + `env:"LIVE_MAX_STREAMS_PER_USER" default:"8"` + "`" + `
 }
 if err := config.Load(&liveEnv); err != nil {
 	return err
 }
 liveHub, err := kitlive.New(pool,
 	kitlive.WithMaxStreams(liveEnv.MaxStreams),
 	kitlive.WithMaxStreamsPerUser(liveEnv.MaxStreamsPerUser),
 )
 if err != nil {
 	return err
 }
 if err := liveHub.Start(ctx); err != nil {
 	return err
 }
Next: add these options to app.New(...), after the apikeys options:
 app.WithRequestTimeoutExempt(kitlive.IsStream),
 app.WithRoutes(applive.Routes(liveHub, applive.Deps{DB: pool})),
 app.WithCommand("live", liveHub.Command()),
 app.WithReadiness("live", liveHub.Ready),
 app.WithDrain(liveHub.Drain),
Next: add app.WithShutdown(liveHub.Close), after the hook that closes the pool.
Next: add a feed with spinup g live <resource>, or spinup g resource <Name> --live.
`

const liveNodePageStep = `Next: in a page, call liveRefresh('<feed>') from '#lib/live.svelte.js'. See docs/build/live.md.
`

const liveEmbedPageStep = `Next: in a page, call liveRefresh('<feed>', { invalidate: RESOURCE_DEP }) from '#lib/live.svelte.js'.
 RESOURCE_DEP is the dependency that the page load registers with depends(). A generated resource list does this.
 See docs/build/live.md.
`

const liveTailSteps = `Next: run spinup doctor and inspect the live-wired check.
`
