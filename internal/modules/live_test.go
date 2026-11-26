package modules

import (
	"strings"
	"testing"
)

func TestLiveNextSteps(t *testing.T) {
	t.Parallel()
	for _, want := range []string{
		"spinup db migrate up", "live_feeds", "live_outbox",
		`applive "<module>/api/internal/live"`,
		`kitlive "github.com/stopyellingatme/spinup/kit/x/live"`,
		`env:"LIVE_MAX_STREAMS" default:"2000"`,
		`env:"LIVE_MAX_STREAMS_PER_USER" default:"8"`,
		"kitlive.New(pool,", "kitlive.WithMaxStreams(liveEnv.MaxStreams)",
		"kitlive.WithMaxStreamsPerUser(liveEnv.MaxStreamsPerUser)", "liveHub.Start(ctx)",
		"app.WithRequestTimeoutExempt(kitlive.IsStream)",
		"app.WithRoutes(applive.Routes(liveHub, applive.Deps{DB: pool}))",
		`app.WithCommand("live", liveHub.Command())`,
		`app.WithReadiness("live", liveHub.Ready)`, "app.WithDrain(liveHub.Drain)",
		"app.WithShutdown(liveHub.Close)",
		"spinup g live <resource>", "spinup g resource <Name> --live",
		"liveRefresh('<feed>')", "spinup doctor", "live-wired",
	} {
		if !strings.Contains(LiveNextSteps(), want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestLiveNextStepsFor(t *testing.T) {
	t.Parallel()
	if LiveNextStepsFor("node") != LiveNextSteps() || LiveNextStepsFor(nil) != LiveNextSteps() {
		t.Fatal("node advice changed")
	}
	embed := LiveNextStepsFor("embed")
	if !strings.Contains(embed, "liveRefresh('<feed>', { invalidate: RESOURCE_DEP })") || strings.Contains(embed, "liveRefresh('<feed>') ") {
		t.Fatalf("embed advice=%s", embed)
	}
	if !strings.Contains(embed, "app.WithDrain(liveHub.Drain)") || !strings.HasSuffix(embed, "Next: run spinup doctor and inspect the live-wired check.\n") {
		t.Fatalf("embed advice lost the Go steps: %s", embed)
	}
}
