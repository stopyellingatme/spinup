package server

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"

	runexec "github.com/stopyellingatme/spinup/internal/exec"
	"github.com/stopyellingatme/spinup/internal/remote"
)

var serviceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// Logs streams logs from matching services in the base and both color projects.
func Logs(ctx context.Context, o Options, service string, runner runexec.Runner) error {
	t, err := load(o)
	if err != nil {
		return err
	}
	r := remote.New(t.host, runner, remote.Options{AcceptNewHostKey: o.AcceptNewHostKey})
	defer func() { _ = r.Close(ctx) }()
	return LogsHost(ctx, r, t.app, service, o)
}

// LogsHost streams Docker logs without evaluating Compose image or secret variables.
func LogsHost(ctx context.Context, r remote.Runner, app, service string, o Options) error {
	if !appName.MatchString(app) || !serviceName.MatchString(service) || strings.ContainsAny(o.Since, "\x00\r\n") {
		return fmt.Errorf("read server logs: invalid app, service, or --since value\nTry: use a service name and --since 10m")
	}
	result, err := r.Run(ctx, remote.Cmd{Argv: []string{"docker", "ps", "--filter", "label=com.docker.compose.service=" + service, "--format", `{{.ID}} {{.Label "com.docker.compose.project"}}`}})
	if err != nil {
		return fmt.Errorf("find service containers: %w\nTry: check Docker access on the server", err)
	}
	ids := make([]string, 0)
	for line := range strings.SplitSeq(string(result.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || (fields[1] != app && fields[1] != app+"-blue" && fields[1] != app+"-green") {
			continue
		}
		if !containerID(fields[0]) {
			return fmt.Errorf("read server logs: Docker returned an invalid container ID\nTry: check Docker on the server")
		}
		ids = append(ids, fields[0])
	}
	if len(ids) == 0 {
		return fmt.Errorf("read server logs: no running containers for service %q\nTry: deploy the service or select another service", service)
	}
	sort.Strings(ids)
	// Following replicas requires concurrent SSH commands. Serialize writes to shared streams.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	stdout, stderr := lockedWriter{&mu, output(o.Out)}, lockedWriter{&mu, output(o.ErrOut)}
	run := func(id string) error {
		args := []string{"docker", "logs"}
		if o.Follow {
			args = append(args, "--follow")
		}
		if o.Since != "" {
			args = append(args, "--since", o.Since)
		}
		args = append(args, id)
		_, err := r.Run(ctx, remote.Cmd{Argv: args, Stdout: stdout, Stderr: stderr})
		if err != nil {
			return fmt.Errorf("read server logs: %w\nTry: check the service and --since value", err)
		}
		return nil
	}
	if !o.Follow {
		for _, id := range ids {
			if err := run(id); err != nil {
				return err
			}
		}
		return nil
	}
	var wg sync.WaitGroup
	var first error
	for _, id := range ids {
		wg.Go(func() {
			if err := run(id); err != nil {
				mu.Lock()
				if first == nil {
					first = err
					cancel()
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return first
}

type lockedWriter struct {
	mu     *sync.Mutex
	writer io.Writer
}

func (w lockedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(data)
}

// SSH opens the remote user's interactive login shell through internal/exec.
func SSH(ctx context.Context, o Options, runner runexec.Runner) error {
	t, err := load(o)
	if err != nil {
		return err
	}
	r := remote.New(t.host, shellRunner{runner}, remote.Options{AcceptNewHostKey: o.AcceptNewHostKey})
	defer func() { _ = r.Close(ctx) }()
	_, err = r.Run(ctx, remote.Cmd{Argv: []string{"sh", "-c", `exec "${SHELL:-/bin/sh}" -l`}, Stdin: o.In, Stdout: o.Out, Stderr: o.ErrOut})
	return err
}

type shellRunner struct{ runner runexec.Runner }

func (r shellRunner) Run(ctx context.Context, c runexec.Cmd) (runexec.Result, error) {
	control := false
	for _, arg := range c.Args {
		if arg == "-O" {
			control = true
			break
		}
	}
	if !control {
		c.Interactive = true
		c.Args = append([]string{"-t"}, c.Args...)
	}
	return r.runner.Run(ctx, c)
}
