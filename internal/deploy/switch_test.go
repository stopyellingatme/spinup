package deploy

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
	"github.com/stopyellingatme/spinup/internal/ui"
)

func TestUpstreamColor(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		data []byte
		want string
	}{
		{UpstreamFile(Blue), Blue},
		{UpstreamFile(Green), Green},
		{PlaceholderUpstream, ""},
		{nil, ""},
		{[]byte("name {args[0]}-red\n"), ""},
	} {
		if got := LiveColor(tt.data); got != tt.want {
			t.Errorf("LiveColor(%q) = %q, want %q", tt.data, got, tt.want)
		}
	}
	if OtherColor(Blue) != Green || OtherColor(Green) != Blue || OtherColor("") != Blue {
		t.Fatal("OtherColor")
	}
	if !strings.Contains(string(UpstreamFile(Green)), "dial_timeout 250ms") || strings.Contains(string(UpstreamFile(Green)), "fail_duration") {
		t.Fatal("upstream options do not follow ADR 0003")
	}
}

type switchFx struct {
	vm     *remote.Fake
	out    bytes.Buffer
	sleeps []time.Duration
	o      SwitchOptions
}

func newSwitchFx() *switchFx {
	s := &switchFx{vm: &remote.Fake{}}
	s.o = SwitchOptions{App: "todo", ReleaseDir: testRelDir, Color: Green, HealthTimeout: 2 * time.Second, Out: &s.out, Redact: []string{"hunter2-db-pass"},
		Now:   func() time.Time { return testNow },
		Sleep: func(_ context.Context, d time.Duration) error { s.sleeps = append(s.sleeps, d); return nil }}
	return s
}

func (s *switchFx) colorArgv(color string, args ...string) []string {
	return ComposeArgv("todo-"+color, testRelDir, []string{testRelDir + "/compose.yml"}, args...)
}

func (s *switchFx) start(upstream string) {
	s.vm.Expect("sh", "-c", readUpstreamScript, "spinup-upstream", testRoot+"/caddy/upstream.caddy").Return(upstream, "", 0)
	s.vm.Expect(psArgv("todo", "caddy")...).Return("caddy1\n", "", 0)
	s.vm.Expect(s.colorArgv(Green, "up", "-d", "--wait", "--wait-timeout", "2", "--remove-orphans")...).Return("", "", 0)
}

func (s *switchFx) ready() {
	s.vm.Expect(psArgv("todo-green", "api")...).Return("napi1\n", "", 0)
	s.vm.Expect(inspectArgv("napi1")...).Return("10.0.1.1\n", "", 0)
	s.vm.Expect(wgetReady("10.0.1.1", 8080, "/readyz")...).Return("", "", 0)
}

func TestSwitchReloadFailureRestoresUpstream(t *testing.T) {
	t.Parallel()
	s := newSwitchFx()
	previous := string(UpstreamFile(Blue))
	s.start(previous)
	s.ready()
	s.vm.Expect(psArgv("todo-blue", "")...).Return("", "", 0)
	s.vm.ExpectUpload(testRelDir+"/upstream.caddy").Return("", "", 0)
	s.vm.ExpectUpload(testRoot+"/caddy/upstream.caddy").Return("", "", 0)
	s.vm.Expect("docker", "exec", "caddy1", "caddy", "reload").Return("", "Error: adapting config", 1)
	s.vm.ExpectUpload(testRoot+"/caddy/upstream.caddy").Return("", "", 0)
	s.vm.Expect(s.colorArgv(Green, "down", "-t", "15")...).Return("", "", 0)
	err := Switch(context.Background(), s.vm, s.o)
	if err == nil || !strings.Contains(err.Error(), "Caddy keeps the old configuration") {
		t.Fatalf("error = %v", err)
	}
	s.vm.AssertDone(t)
	uploads := s.vm.Uploads()
	if got := string(uploads[len(uploads)-1].Data); got != previous {
		t.Fatalf("restored upstream = %q", got)
	}
}

func TestSwitchNeverStopsTheLiveColor(t *testing.T) {
	t.Parallel()
	s := newSwitchFx()
	// A resumed switch: Caddy already routes to green, and green is not healthy.
	s.start(string(UpstreamFile(Green)))
	s.vm.Expect(psArgv("todo-green", "api")...).Return("", "", 0)
	err := Switch(context.Background(), s.vm, s.o)
	if err == nil || !strings.Contains(err.Error(), "is live but not healthy") {
		t.Fatalf("error = %v", err)
	}
	s.vm.AssertDone(t)
	for _, c := range s.vm.Calls() {
		if strings.Contains(strings.Join(c.Argv, " "), " down ") {
			t.Fatalf("stopped the live color: %v", c.Argv)
		}
	}
}

func TestSwitchUpFailureStopsNewColor(t *testing.T) {
	t.Parallel()
	s := newSwitchFx()
	s.vm.Expect("sh", "-c", readUpstreamScript).Return(string(UpstreamFile(Blue)), "", 0)
	s.vm.Expect(psArgv("todo", "caddy")...).Return("caddy1\n", "", 0)
	s.vm.Expect(s.colorArgv(Green, "up")...).Return("", "container todo-green-api-1 is unhealthy", 1)
	s.vm.Expect(s.colorArgv(Green, "down", "-t", "15")...).Return("", "", 0)
	err := Switch(context.Background(), s.vm, s.o)
	var health *HealthError
	if !errors.As(err, &health) || !health.Stopped || !strings.Contains(err.Error(), "old color is still live") {
		t.Fatalf("error = %v", err)
	}
	s.vm.AssertDone(t)
}

func TestSwitchDrainStopsAtMaximum(t *testing.T) {
	t.Parallel()
	s := newSwitchFx()
	s.start(string(UpstreamFile(Blue)))
	s.ready()
	s.vm.Expect(psArgv("todo-blue", "")...).Return("o1\n", "", 0)
	s.vm.Expect(inspectArgv("o1")...).Return("10.0.0.9\n", "", 0)
	s.vm.ExpectUpload(testRelDir+"/upstream.caddy").Return("", "", 0)
	s.vm.ExpectUpload(testRoot+"/caddy/upstream.caddy").Return("", "", 0)
	s.vm.Expect("docker", "exec", "caddy1", "caddy", "reload").Return("", "", 0)
	busy := `[{"address":"10.0.0.9:8080","num_requests":1}]`
	polls := 1 + int((drainMax-drainMin)/drainInterval)
	for i := range polls {
		if i%2 == 0 {
			s.vm.Expect("docker", "exec", "caddy1", "wget").Return(busy, "", 0)
		} else {
			// A failed poll counts as busy.
			s.vm.Expect("docker", "exec", "caddy1", "wget").Return("", "", 1)
		}
	}
	s.vm.Expect(s.colorArgv(Blue, "down", "-t", "15", "--remove-orphans")...).Return("", "", 0)
	if err := Switch(context.Background(), s.vm, s.o); err != nil {
		t.Fatal(err)
	}
	s.vm.AssertDone(t)
	var total time.Duration
	for _, d := range s.sleeps {
		total += d
	}
	if total != drainMax || s.sleeps[0] != drainMin {
		t.Fatalf("drain waits = %v", s.sleeps)
	}
}

func TestSwitchInputValidation(t *testing.T) {
	t.Parallel()
	for _, o := range []SwitchOptions{
		{App: "../x", ReleaseDir: testRelDir, Color: Green},
		{App: "todo", ReleaseDir: testRelDir, Color: "red"},
		{App: "todo", ReleaseDir: "/tmp/x", Color: Green},
	} {
		f := &remote.Fake{}
		if err := Switch(context.Background(), f, o); err == nil || len(f.Calls()) != 0 {
			t.Errorf("Switch(%+v) = %v", o, err)
		}
	}
}

func TestSwitchNoCaddy(t *testing.T) {
	t.Parallel()
	s := newSwitchFx()
	s.vm.Expect("sh", "-c", readUpstreamScript).Return("", "", 0)
	s.vm.Expect(psArgv("todo", "caddy")...).Return("", "", 0)
	if err := Switch(context.Background(), s.vm, s.o); err == nil || !strings.Contains(err.Error(), "Caddy container") {
		t.Fatalf("error = %v", err)
	}
	s.vm.AssertDone(t)
}

func TestHostPort(t *testing.T) {
	t.Parallel()
	if hostPort("10.0.0.1", 80) != "10.0.0.1:80" || hostPort("fd00::1", 80) != "[fd00::1]:80" {
		t.Fatal("hostPort")
	}
}

func TestSwitchUnknownReloadPreservesBothColors(t *testing.T) {
	t.Parallel()
	for _, cause := range []error{context.Canceled, errors.New("SSH session dropped"), &runexec.ExitError{Code: 255}, &runexec.ExitError{Code: -1}} {
		t.Run(cause.Error(), func(t *testing.T) {
			t.Parallel()
			s := newSwitchFx()
			s.start(string(UpstreamFile(Blue)))
			s.ready()
			s.vm.Expect(psArgv("todo-blue", "")...).Return("", "", 0)
			s.vm.ExpectUpload(testRelDir+"/upstream.caddy").Return("", "", 0)
			s.vm.ExpectUpload(testRoot+"/caddy/upstream.caddy").Return("", "", 0)
			s.vm.Expect("docker", "exec", "caddy1", "caddy", "reload").Fail(cause)
			fresh := &remote.Fake{}
			s.o.Reconnect = func(ctx context.Context) (remote.Runner, error) {
				if ctx.Err() != nil {
					t.Errorf("inspection context canceled: %v", ctx.Err())
				}
				return fresh, nil
			}
			fresh.Expect("sh", "-c", readUpstreamScript).Return(string(UpstreamFile(Green)), "", 0)
			fresh.Expect(psArgv("todo-blue", "")...).Return("old\n", "", 0)
			fresh.Expect(psArgv("todo-green", "")...).Return("new\n", "", 0)
			fresh.ExpectClose().Return("", "", 0)
			err := Switch(context.Background(), s.vm, s.o)
			if err == nil || !strings.Contains(err.Error(), "unknown") || !strings.Contains(err.Error(), "Try: spinup deploy status, then spinup deploy --resume") {
				t.Errorf("error=%v", err)
			}
			for _, c := range s.vm.Calls() {
				if strings.Contains(strings.Join(c.Argv, " "), " down ") {
					t.Errorf("stopped a color after uncertain reload: %v", c.Argv)
				}
			}
			s.vm.AssertDone(t)
			fresh.AssertDone(t)
			if len(s.vm.Uploads()) != 2 {
				t.Error("rewrote the upstream after uncertain reload")
			}
		})
	}
}

func TestSwitchUnknownReloadInspectionFailure(t *testing.T) {
	t.Parallel()
	s := newSwitchFx()
	s.vm.ExpectClose().Return("", "", 0)
	s.vm.Expect("sh", "-c", readUpstreamScript).Fail(errors.New("SSH unavailable"))
	s.vm.Expect(psArgv("todo-blue", "")...).Fail(errors.New("SSH unavailable"))
	s.vm.Expect(psArgv("todo-green", "")...).Fail(errors.New("SSH unavailable"))
	switcher := &switcher{r: s.vm, o: s.o, root: testRoot}
	switcher.defaults()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := switcher.reloadUnknown(ctx, context.Canceled)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("error=%v", err)
	}
	wrapped := deployError("switch", "staging", err)
	var ue *ui.Error
	if !errors.As(wrapped, &ue) || ue.Try != "spinup deploy status, then spinup deploy --resume" || !strings.Contains(wrapped.Error(), "unknown") {
		t.Fatalf("wrapped error=%v", wrapped)
	}
	s.vm.AssertDone(t)
}
