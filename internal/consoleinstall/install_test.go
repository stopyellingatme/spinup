package consoleinstall

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/internal/remote"
)

func installSpec() Spec {
	return Spec{App: "sample", Env: "production", Profile: "absent", Backend: "none", Origin: "https://sample.example", Image: "sample-api:release"}
}

func TestConsoleRefresh(t *testing.T) {
	r := &remote.Fake{}
	r.Expect("/opt/sample/console/collect.sh", "--if-old").Return("", "", 0)
	if err := Refresh(context.Background(), r, "sample"); err != nil {
		t.Fatal(err)
	}
	r.AssertDone(t)
	if err := Refresh(context.Background(), r, "../sample"); err == nil {
		t.Fatal("unsafe app accepted")
	}
}

func TestConsoleReconcileInactive(t *testing.T) {
	r := &remote.Fake{}
	r.ExpectUpload("/opt/sample/console/compose.query.yml").Return("", "", 0)
	r.Expect("docker", "ps", "-a", "--quiet", "--no-trunc", "--filter", "label=com.docker.compose.project=sample-console", "--filter", "label=com.docker.compose.service=console-query").Return(strings.Repeat("a", 64)+"\n", "", 0)
	r.Expect("docker", "rm", "-f", "--", strings.Repeat("a", 64)).Return("", "", 0)
	if err := ReconcileQuery(context.Background(), r, installSpec(), []byte("services: {}\n")); err != nil {
		t.Fatal(err)
	}
	r.AssertDone(t)
}

func composeFixture(t *testing.T, profile string) (Spec, []byte, []byte) {
	t.Helper()
	s := installSpec()
	s.Profile = profile
	base, err := os.ReadFile("../../testdata/golden/console/absent/console/compose.console.yml")
	if err != nil {
		t.Fatal(err)
	}
	query, err := os.ReadFile("../../testdata/golden/console/" + profile + "/console/compose.query.yml")
	if err != nil {
		t.Fatal(err)
	}
	return s, bytes.ReplaceAll(base, []byte("todo"), []byte(s.App)), bytes.ReplaceAll(query, []byte("todo"), []byte(s.App))
}

func expectInstall(r *remote.Fake, s Spec, mapping string, timer bool) {
	dir := "/opt/" + s.App + "/console"
	stage := dir + "/.install.fixture"
	r.Expect("sh", "-c", prepareScript).Return(stage+"\n", "", 0)
	for _, name := range []string{"compose.console.yml", ".env", "probe.conf", "collect.sh"} {
		r.ExpectUpload(stage+"/"+name).Return("", "", 0)
	}
	r.Expect("sh", "-c", publishScript).Return("", "", 0)
	r.Expect(compose(s, false, "up", "-d", "--no-deps", "--wait", "console")...).Return("", "", 0)
	r.Expect(compose(s, false, "ps", "--all", "--quiet", "console")...).Return(strings.Repeat("a", 64)+"\n", "", 0)
	r.Expect("docker", "inspect").Return(mapping, "", 0)
	if timer {
		r.Expect("sh", "-c", timerProbeScript).Return("yes\n", "", 0)
		r.Expect("sh", "-c", timerDirScript).Return("/home/deploy/.config/systemd/user\n", "", 0)
		for _, ext := range []string{"service", "timer"} {
			r.ExpectUpload("/home/deploy/.config/systemd/user/spinup-"+s.App+"-console-sample."+ext).Return("", "", 0)
		}
		r.Expect("systemctl", "--user", "daemon-reload").Return("", "", 0)
		r.Expect("systemctl", "--user", "enable", "--now", "spinup-"+s.App+"-console-sample.timer").Return("", "", 0)
	} else if mapping == loopbackMapping {
		r.Expect("sh", "-c", timerProbeScript).Return("", "", 0)
	}
	r.Expect("rm", "-rf", "--", stage).Return("", "", 0)
}

const loopbackMapping = `{"7410/tcp":[{"HostIp":"127.0.0.1","HostPort":"7410"}]}`

func TestConsoleInstall(t *testing.T) {
	for _, timer := range []bool{false, true} {
		t.Run(strconv.FormatBool(timer), func(t *testing.T) {
			s, base, overlay := composeFixture(t, "absent")
			if err := Validate(s, base, overlay); err != nil {
				t.Fatal(err)
			}
			r := &remote.Fake{}
			expectInstall(r, s, loopbackMapping, timer)
			if err := Install(context.Background(), r, s, base); err != nil {
				t.Fatal(err)
			}
			r.AssertDone(t)
			uploads := r.Uploads()
			if !bytes.Equal(uploads[3].Data, collector) || uploads[3].Mode != 0700 || uploads[1].Mode != 0600 || uploads[2].Mode != 0600 {
				t.Fatal("incorrect script or config mode")
			}
			if string(uploads[2].Data) != "sample\nproduction\nabsent\nnone\nfalse\nsample-api:release\nhttps://sample.example\n" {
				t.Fatalf("probe configuration = %s", uploads[2].Data)
			}
			for _, u := range uploads {
				if strings.Contains(u.Path, "/data/") {
					t.Fatal("installer replaced a snapshot")
				}
			}
			for _, c := range r.Calls() {
				if len(c.Argv) > 0 && strings.HasSuffix(c.Argv[0], "collect.sh") {
					t.Fatal("installer sampled recursively")
				}
			}
		})
	}
}

func TestConsoleInstallActualMapping(t *testing.T) {
	for _, mapping := range []string{`{}`, `{"7410/tcp":[{"HostIp":"0.0.0.0","HostPort":"7410"}]}`, `{"7410/tcp":[{"HostIp":"127.0.0.1","HostPort":"7411"}]}`, `{"7410/tcp":[{"HostIp":"127.0.0.1","HostPort":"7410"},{"HostIp":"::","HostPort":"7410"}]}`} {
		s, base, _ := composeFixture(t, "absent")
		r := &remote.Fake{}
		expectInstall(r, s, mapping, false)
		if err := Install(context.Background(), r, s, base); err == nil {
			t.Fatal("unsafe actual mapping accepted")
		}
		r.AssertDone(t)
	}
}

func TestConsoleInstallValidation(t *testing.T) {
	for _, profile := range []string{"absent", "none", "lite", "full"} {
		s, base, overlay := composeFixture(t, profile)
		if err := Validate(s, base, overlay); err != nil {
			t.Fatalf("%s: %v", profile, err)
		}
		for _, mutation := range []struct{ old, new string }{{"127.0.0.1:7410", "0.0.0.0:7410"}, {"user: \"65532:65532\"", "user: \"0:0\""}, {"/data:ro", "/data:rw"}, {"read_only: true", "read_only: false"}, {"mem_limit: 64m", "mem_limit: 128m"}, {"networks: [console]", "networks: [console, monitoring]"}, {"services:\n", "services:\n  other: {}\n"}} {
			r := &remote.Fake{}
			bad := bytes.Replace(base, []byte(mutation.old), []byte(mutation.new), 1)
			if err := Install(context.Background(), r, s, bad); err == nil {
				t.Fatalf("accepted %s", mutation.new)
			}
			if len(r.Calls()) > 0 || len(r.Uploads()) > 0 {
				t.Fatal("changed VM before validation")
			}
		}
		if err := Validate(s, base, append(overlay, []byte("---\nservices: {}\n")...)); err == nil {
			t.Fatal("accepted extra Compose document")
		}
	}
	for _, mutate := range []func(*Spec){func(s *Spec) { s.App = "../app" }, func(s *Spec) { s.Env = "prod\n" }, func(s *Spec) { s.Image = "" }, func(s *Spec) { s.Image = "api; id" }, func(s *Spec) { s.Profile = "unknown" }, func(s *Spec) { s.Backend = "unknown" }, func(s *Spec) { s.Origin = "https://user:pass@example.com" }, func(s *Spec) { s.Origin = "https://example.com/\n" }} {
		s, base, _ := composeFixture(t, "absent")
		mutate(&s)
		r := &remote.Fake{}
		if err := Install(context.Background(), r, s, base); err == nil || len(r.Calls()) > 0 {
			t.Fatal("invalid spec changed VM")
		}
	}
}

func TestConsoleReconcileActive(t *testing.T) {
	s, _, overlay := composeFixture(t, "lite")
	r := &remote.Fake{}
	r.ExpectUpload("/opt/sample/console/compose.query.yml").Return("", "", 0)
	r.Expect(compose(s, true, "up", "-d", "--no-deps", "--wait", "console-query")...).Return("", "", 0)
	if err := ReconcileQuery(context.Background(), r, s, overlay); err != nil {
		t.Fatal(err)
	}
	r.AssertDone(t)
}

func TestConsoleInstallInterruptedUpload(t *testing.T) {
	s, base, _ := composeFixture(t, "absent")
	r := &remote.Fake{}
	stage := "/opt/sample/console/.install.fixture"
	r.Expect("sh", "-c", prepareScript).Return(stage+"\n", "", 0)
	r.ExpectUpload(stage+"/compose.console.yml").Return("", "", 0)
	r.ExpectUpload(stage+"/.env").Return("", "interrupted upload", 1)
	r.Expect("rm", "-rf", "--", stage).Return("", "", 0)
	if err := Install(context.Background(), r, s, base); err == nil || !strings.Contains(err.Error(), "upload console/.env") {
		t.Fatalf("error = %v", err)
	}
	r.AssertDone(t)
	for _, call := range r.Calls() {
		if len(call.Argv) > 2 && call.Argv[2] == publishScript {
			t.Fatal("interrupted upload published partial config")
		}
	}
}

func TestConsoleRefreshInitial(t *testing.T) {
	r := &remote.Fake{}
	r.Expect("sh", "-c", initialSampleScript, "spinup-console-sample", "/opt/sample/console/collect.sh").Return("", "", 0)
	if err := StartSample(context.Background(), r, "sample"); err != nil {
		t.Fatal(err)
	}
	r.AssertDone(t)
}
