// Package browser opens a URL in the user's web browser (ADR 0017).
package browser

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
)

// systemTimeout bounds a system opener. These programs return at once.
const systemTimeout = 10 * time.Second

// overrideTimeout bounds a command from SPINUP_BROWSER.
// A test command such as "curl -fsSL" can wait for a slow callback.
const overrideTimeout = 2 * time.Minute

// Test seams. Production code uses the real values.
var (
	goos      = goosValue()
	getenv    = os.Getenv
	fileExist = func(path string) bool { _, err := os.Stat(path); return err == nil }
	onPath    = func(name string) bool { _, err := exec.LookPath(name); return err == nil }
)

// Open starts a browser on rawURL through r and returns when the opener exits.
//
// The order is: SPINUP_BROWSER (a command line split on spaces, with the URL
// as the last argument), then the system opener. The system opener is "open"
// on darwin, "rundll32 url.dll,FileProtocolHandler" on windows, "wslview" on
// WSL when it is on PATH, and "xdg-open" otherwise.
// Only http and https URLs are valid. The caller must print the URL anyway.
func Open(ctx context.Context, r runexec.Runner, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("cannot open %q in a browser: the URL must start with http:// or https://.\nTry: pass a full http or https URL", rawURL)
	}
	argv, timeout := command(getenv, rawURL)
	_, err = r.Run(ctx, runexec.Cmd{Name: argv[0], Args: argv[1:], Timeout: timeout})
	if err != nil {
		return fmt.Errorf("cannot open the browser with %q: %w.\nTry: open the URL by hand, or set SPINUP_BROWSER to a working command", argv[0], err)
	}
	return nil
}

func command(env func(string) string, rawURL string) ([]string, time.Duration) {
	if fields := strings.Fields(env("SPINUP_BROWSER")); len(fields) > 0 {
		return append(fields, rawURL), overrideTimeout
	}
	switch goos {
	case "darwin":
		return []string{"open", rawURL}, systemTimeout
	case "windows":
		return []string{"rundll32", "url.dll,FileProtocolHandler", rawURL}, systemTimeout
	}
	if isWSL() && onPath("wslview") {
		return []string{"wslview", rawURL}, systemTimeout
	}
	return []string{"xdg-open", rawURL}, systemTimeout
}

func isWSL() bool {
	return goos == "linux" && (fileExist("/proc/sys/fs/binfmt_misc/WSLInterop") || getenv("WSL_DISTRO_NAME") != "")
}
