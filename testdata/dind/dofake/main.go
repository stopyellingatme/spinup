// Command dofake serves the DigitalOcean API fake for the dind suite (docs/contributing/dind.md).
//
// It prints its URL as the first line of stdout and serves until it is killed. With -on-create,
// a droplet create runs a program that makes the dind VM look like the new droplet.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/stopyellingatme/spinup/internal/cloud/digitalocean/dofake"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "dofake: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:0", "listen address")
	ipv4 := flag.String("ipv4", "127.0.0.1", "IPv4 address of new droplets")
	token := flag.String("token", "", "static access token that the API accepts")
	onCreate := flag.String("on-create", "", "executable that runs with the staging directory of a droplet create")
	flag.Parse()
	if *token == "" {
		return errors.New("-token is required\nTry: pass -token <static token>")
	}

	opts := dofake.Options{IPv4: *ipv4, StaticTokens: []string{*token}}
	if *onCreate != "" {
		hook := *onCreate
		opts.OnCreateDroplet = func(req map[string]any) error { return runHook(hook, req) }
	}
	fake := dofake.New(opts)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", *addr, err)
	}
	mux := http.NewServeMux()
	// The debug route exists only in this binary. The dofake package does not have it.
	mux.HandleFunc("/debug/state", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"droplets": len(fake.Droplets()), "firewalls": len(fake.Firewalls()), "ssh_keys": len(fake.SSHKeys()),
		})
	})
	mux.Handle("/", fake.Handler())
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	fmt.Printf("http://%s\n", ln.Addr())
	_ = os.Stdout.Sync()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// runHook extracts the user data of a create into a staging directory and runs the program.
// The directory holds user-data.yaml, ssh_host_ed25519_key(.pub), manifest (lines "<mode> <path>"),
// and root/<path> for every write_files entry.
func runHook(hook string, req map[string]any) error {
	data, _ := req["user_data"].(string)
	dir, err := os.MkdirTemp("", "dofake-create-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := stage(dir, data); err != nil {
		return err
	}
	out, err := exec.Command(hook, dir).CombinedOutput() //nolint:gosec // the hook path is a flag of the test tool
	if err != nil {
		return fmt.Errorf("the on-create hook failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

type userData struct {
	SSHKeys struct {
		Private string `yaml:"ed25519_private"`
		Public  string `yaml:"ed25519_public"`
	} `yaml:"ssh_keys"`
	WriteFiles []struct {
		Path        string `yaml:"path"`
		Content     string `yaml:"content"`
		Permissions string `yaml:"permissions"`
		Encoding    string `yaml:"encoding"`
	} `yaml:"write_files"`
}

func stage(dir, data string) error {
	if err := os.WriteFile(filepath.Join(dir, "user-data.yaml"), []byte(data), 0o600); err != nil {
		return err
	}
	var ud userData
	if err := yaml.Unmarshal([]byte(data), &ud); err != nil {
		return fmt.Errorf("parse the user data: %w", err)
	}
	if ud.SSHKeys.Private == "" || ud.SSHKeys.Public == "" {
		return errors.New("the user data has no ssh_keys.ed25519_private and ed25519_public")
	}
	nl := func(s string) string { return strings.TrimRight(s, "\n") + "\n" }
	if err := os.WriteFile(filepath.Join(dir, "ssh_host_ed25519_key"), []byte(nl(ud.SSHKeys.Private)), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "ssh_host_ed25519_key.pub"), []byte(nl(ud.SSHKeys.Public)), 0o600); err != nil {
		return err
	}
	var manifest strings.Builder
	for _, f := range ud.WriteFiles {
		content := []byte(f.Content)
		if f.Encoding == "b64" || f.Encoding == "base64" {
			b, err := base64.StdEncoding.DecodeString(f.Content)
			if err != nil {
				return fmt.Errorf("decode %s: %w", f.Path, err)
			}
			content = b
		}
		mode := uint64(0o644)
		if f.Permissions != "" {
			m, err := strconv.ParseUint(f.Permissions, 8, 32)
			if err != nil {
				return fmt.Errorf("permissions %q of %s: %w", f.Permissions, f.Path, err)
			}
			mode = m
		}
		// Clean on a rooted path removes any "..", so the file stays under root/.
		rel := strings.TrimPrefix(filepath.Clean("/"+f.Path), "/")
		dst := filepath.Join(dir, "root", rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(dst, content, 0o600); err != nil {
			return err
		}
		fmt.Fprintf(&manifest, "%04o /%s\n", mode, rel)
	}
	return os.WriteFile(filepath.Join(dir, "manifest"), []byte(manifest.String()), 0o600)
}
