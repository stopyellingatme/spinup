package browser

import (
	"context"
	"strings"
	"testing"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

func setup(t *testing.T, os string, env map[string]string, wslFile, wslview bool) {
	t.Helper()
	og, oe, of, op := goos, getenv, fileExist, onPath
	t.Cleanup(func() { goos, getenv, fileExist, onPath = og, oe, of, op })
	goos = os
	getenv = func(k string) string { return env[k] }
	fileExist = func(string) bool { return wslFile }
	onPath = func(string) bool { return wslview }
}

func TestOpenBranches(t *testing.T) {
	const u = "https://example.com/a?b=1"
	tests := []struct {
		name    string
		os      string
		env     map[string]string
		file    bool
		wslview bool
		want    []string
		timeout time.Duration
	}{
		{"darwin", "darwin", nil, false, false, []string{"open", u}, systemTimeout},
		{"windows", "windows", nil, false, false, []string{"rundll32", "url.dll,FileProtocolHandler", u}, systemTimeout},
		{"linux", "linux", nil, false, false, []string{"xdg-open", u}, systemTimeout},
		{"wsl file with wslview", "linux", nil, true, true, []string{"wslview", u}, systemTimeout},
		{"wsl env with wslview", "linux", map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, false, true, []string{"wslview", u}, systemTimeout},
		{"wsl without wslview", "linux", nil, true, false, []string{"xdg-open", u}, systemTimeout},
		{"override", "darwin", map[string]string{"SPINUP_BROWSER": "  curl -fsSL "}, false, false, []string{"curl", "-fsSL", u}, overrideTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setup(t, tt.os, tt.env, tt.file, tt.wslview)
			f := &runexec.Fake{}
			f.Expect(tt.want[0], tt.want[1:]...)
			if err := Open(context.Background(), f, u); err != nil {
				t.Fatal(err)
			}
			f.AssertDone(t)
			if got := f.Calls()[0].Timeout; got != tt.timeout {
				t.Errorf("timeout = %v, want %v", got, tt.timeout)
			}
		})
	}
}

func TestOpenRejectsScheme(t *testing.T) {
	setup(t, "linux", nil, false, false)
	for _, u := range []string{"file:///etc/passwd", "javascript:alert(1)", "ftp://x/y", "example.com", "http://"} {
		f := &runexec.Fake{}
		err := Open(context.Background(), f, u)
		if err == nil || !strings.Contains(err.Error(), "Try:") {
			t.Errorf("%q: err = %v", u, err)
		}
		if len(f.Calls()) != 0 {
			t.Errorf("%q: opener ran", u)
		}
	}
}

func TestOpenReportsFailure(t *testing.T) {
	setup(t, "linux", nil, false, false)
	f := &runexec.Fake{}
	f.Expect("xdg-open").Return("", "", 3)
	err := Open(context.Background(), f, "http://127.0.0.1:1")
	if err == nil || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("err = %v", err)
	}
}
