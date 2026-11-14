package templates_test

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/deploy"
	spexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/testutil"
)

func TestGoldenLive(t *testing.T) {
	t.Parallel()
	if !testutil.GoldenSelected(t, "live") {
		return
	}
	render := func() (fs.FS, error) {
		return testutil.RenderPack(t, []string{"base", "api", "web", "auth", "live", "deploy"}, map[string]any{
			"app_name": "todo", "module": "example.com/todo", "modules": "auth,live",
		}), nil
	}
	fsys, err := render()
	if err != nil {
		t.Fatal(err)
	}
	main := readDeployFile(t, fsys, "api/cmd/api/main.go")
	if _, err := parser.ParseFile(token.NewFileSet(), "main.go", main, parser.AllErrors); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`applive "example.com/todo/api/internal/live"`,
		`kitlive "github.com/stopyellingatme/spinup/kit/x/live"`,
		`app.WithRequestTimeoutExempt(kitlive.IsStream),`,
		`app.WithRoutes(applive.Routes(liveHub, applive.Deps{DB: pool})),`,
		`app.WithCommand("live", liveHub.Command()),`,
		`app.WithReadiness("live", liveHub.Ready),`,
		`app.WithDrain(liveHub.Drain),`,
		`app.WithShutdown(liveHub.Close),`,
	} {
		if !strings.Contains(main, want) {
			t.Errorf("main.go does not contain %q", want)
		}
	}
	caddyfile := readDeployFile(t, fsys, "deploy/Caddyfile")
	want := "handle /api/live/* {\n\t\timport upstream.caddy api 8080 { flush_interval -1 }\n\t}"
	if strings.Count(caddyfile, want) != 1 || strings.Count(caddyfile, "flush_interval -1") != 1 || strings.Contains(caddyfile, "handle /api/mcp") {
		t.Errorf("Caddyfile must flush only the live route:\n%s", caddyfile)
	}

	alerts := readDeployFile(t, renderMonitoring(t, "lite"), "monitoring/prometheus/rules/app.yml")
	latency := strings.Split(strings.Split(alerts, "- alert: HighLatencyP95")[1], "for: 10m")[0]
	if strings.Count(latency, `http_route!~".*/api/live/.+/live"`) != 2 {
		t.Error("HighLatencyP95 must exclude live streams from both selectors")
	}
	errorRate := strings.Split(strings.Split(alerts, "- alert: HighErrorRate")[1], "for: 10m")[0]
	if strings.Contains(errorRate, "http_route") {
		t.Error("HighErrorRate must include live streams")
	}
	t.Run("caddy-adapt", func(t *testing.T) { liveCaddyAdapt(t, writeDeployTree(t, fsys)) })
	testutil.Golden(t, "live", fsys)
	testutil.Deterministic(t, render)
}

func liveCaddyAdapt(t *testing.T, dir string) {
	t.Helper()
	if _, err := spexec.LookPath("caddy"); err != nil {
		t.Skip("caddy is not on PATH")
	}
	if err := os.WriteFile(filepath.Join(dir, "upstream.caddy"), deploy.UpstreamFile("green"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	result, err := spexec.OSRunner{}.Run(ctx, spexec.Cmd{
		Name: "caddy", Args: []string{"adapt", "--config", "Caddyfile", "--adapter", "caddyfile"},
		Dir: dir, Env: []string{"PUBLIC_HOST=todo.example.com"},
	})
	if err != nil {
		t.Fatalf("caddy adapt: %v\n%s", err, result.Stderr)
	}
	var config any
	if err := json.Unmarshal(result.Stdout, &config); err != nil {
		t.Fatal(err)
	}
	if !hasLiveFlush(config) || strings.Count(string(result.Stdout), `"flush_interval":-1`) != 1 {
		t.Errorf("caddy adapt must flush only the live route:\n%s", result.Stdout)
	}
}

func hasLiveFlush(node any) bool {
	switch node := node.(type) {
	case map[string]any:
		match, err := json.Marshal(node["match"])
		if err == nil && strings.Contains(string(match), `"path":["/api/live/*"]`) {
			handlers, err := json.Marshal(node["handle"])
			return err == nil && strings.Contains(string(handlers), `"flush_interval":-1`)
		}
		for _, child := range node {
			if hasLiveFlush(child) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if hasLiveFlush(child) {
				return true
			}
		}
	}
	return false
}
