package modules

// LiveNextSteps returns the manual wiring steps for an existing app with live data (ADR 0033).
func LiveNextSteps() string {
	return `Next: run spinup db migrate up. It adds the tables live_feeds and live_outbox.
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
Next: in a page, call liveRefresh('<feed>') from '#lib/live.svelte.js'. See docs/build/live.md.
Next: run spinup doctor and inspect the live-wired check.
`
}
