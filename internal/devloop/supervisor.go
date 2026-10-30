package devloop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/stopyellingatme/spinup/internal/exec"
)

// Process describes one long-running dev command and its readiness check.
type Process struct {
	// Name labels log lines. Dir overrides the command working directory.
	Name, Dir string
	// Cmd is the command to start in a separate process group.
	Cmd exec.Cmd
	// Ready must return when ctx is canceled. It runs after every start.
	Ready func(ctx context.Context) error
	// Restart enables retries when a process exits.
	Restart bool
	// Reload stops and starts the process again at once, with no backoff, each time it
	// receives a value. A nil channel never reloads.
	Reload <-chan struct{}
}

// Supervisor streams process logs and owns process groups until all children stop.
// A zero value uses OSStarter, discarded output, and a 250 ms restart backoff.
type Supervisor struct {
	// Starter defaults to OSStarter.
	Starter Starter
	// Out receives complete log lines.
	Out io.Writer
	// NoColor disables prefix colors. NO_COLOR also disables them.
	NoColor bool
	// Backoff is the initial retry delay, doubled up to five seconds.
	Backoff time.Duration
	// OnReady runs once after all processes pass their readiness checks.
	OnReady func()
}

// Run starts processes in order, waits for readiness, and restarts failed children.
// Cancellation stops all groups concurrently, then waits for every child.
func (s Supervisor) Run(ctx context.Context, processes []Process) error {
	if len(processes) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	starter := s.Starter
	if starter == nil {
		starter = OSStarter{}
	}
	out := s.Out
	if out == nil {
		out = io.Discard
	}
	backoff := s.Backoff
	if backoff <= 0 {
		backoff = 250 * time.Millisecond
	}
	width := 4
	for _, process := range processes {
		if len(process.Name) > width {
			width = len(process.Name)
		}
	}
	log := &logMux{out: out, width: width, color: !s.NoColor && os.Getenv("NO_COLOR") == ""}
	var wg sync.WaitGroup
	failures := make(chan error, len(processes))
	defer wg.Wait()
	// Cancellation must run before the deferred wait.
	defer cancel()
	for index, process := range processes {
		ready := make(chan struct{}, 1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := supervise(ctx, starter, process, index, log, backoff, ready)
			if err != nil && (ctx.Err() == nil || !errors.Is(err, ctx.Err())) {
				failures <- err
				cancel()
			}
		}()
		select {
		case <-ready:
		case <-ctx.Done():
			select {
			case err := <-failures:
				return err
			default:
				return nil
			}
		}
	}
	if s.OnReady != nil {
		s.OnReady()
	}

	select {
	case err := <-failures:
		return err
	case <-ctx.Done():
		select {
		case err := <-failures:
			return err
		default:
			return nil
		}
	}
}

func supervise(ctx context.Context, starter Starter, p Process, index int, log *logMux, backoff time.Duration, ready chan<- struct{}) error {
	announced := false
	delay := backoff
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		stdout := &lineWriter{log: log, name: p.Name, index: index}
		stderr := &lineWriter{log: log, name: p.Name, index: index}
		command := p.Cmd
		if p.Dir != "" {
			command.Dir = p.Dir
		}
		command.Stdout, command.Stderr = stdout, stderr
		child, err := starter.Start(ctx, command)
		if err != nil {
			err = fmt.Errorf("start %s: %w\nTry: check the dev command and its tools", p.Name, err)
			return err
		}
		exited := make(chan error, 1)
		go func() { exited <- child.Wait() }()
		var runErr error
		dead := false
		if p.Ready != nil {
			readyCtx, cancelReady := context.WithCancel(ctx)
			checked := make(chan error, 1)
			go func() { checked <- p.Ready(readyCtx) }()
			select {
			case err := <-checked:
				cancelReady()
				if err != nil {
					stopChild(child, exited)
					stdout.flush()
					stderr.flush()
					return fmt.Errorf("%s readiness failed: %w\nTry: inspect the process logs", p.Name, err)
				}
			case runErr = <-exited:
				dead = true
				cancelReady()
				<-checked
			case <-ctx.Done():
				cancelReady()
				stopChild(child, exited)
				<-checked
				stdout.flush()
				stderr.flush()
				return ctx.Err()
			}
		}
		reloaded := false
		if !dead {
			if !announced {
				ready <- struct{}{}
				announced = true
			}
			select {
			case <-ctx.Done():
				stopChild(child, exited)
			case runErr = <-exited:
				dead = true
			case <-p.Reload:
				stopChild(child, exited)
				reloaded = true
			}
		}
		if dead {
			// A crashed leader can leave descendants. Remove them before restart.
			_ = child.Kill()
		}
		stdout.flush()
		stderr.flush()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if reloaded {
			log.line(p.Name, index, "restarting after a Go change")
			delay = backoff
			continue
		}
		if runErr == nil {
			runErr = errors.New("process exited unexpectedly")
		}
		if !p.Restart {
			return fmt.Errorf("%s crashed: %w\nTry: inspect the process logs and repeat spinup dev", p.Name, runErr)
		}
		log.line(p.Name, index, fmt.Sprintf("crashed: %v. Restarting in %s", runErr, delay))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		delay = min(delay*2, 5*time.Second)
	}
}

func stopChild(child Child, exited <-chan error) {
	_ = child.Terminate()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	reaped := false
	for {
		if reaped && !child.Alive() {
			return
		}
		select {
		case <-exited:
			reaped = true
			exited = nil
		case <-ticker.C:
		case <-timer.C:
			_ = child.Kill()
			if !reaped {
				<-exited
			}
			return
		}
	}
}

type logMux struct {
	mu    sync.Mutex
	out   io.Writer
	width int
	color bool
}

func (m *logMux) line(name string, index int, value string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := fmt.Sprintf("%-*s |", m.width, name)
	if m.color {
		prefix = fmt.Sprintf("\x1b[%dm%s\x1b[0m", 31+index%6, prefix)
	}
	_, _ = fmt.Fprintf(m.out, "%s %s\n", prefix, value)
}

type lineWriter struct {
	mu      sync.Mutex
	pending []byte
	log     *logMux
	name    string
	index   int
}

func (w *lineWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, data...)
	for {
		index := bytes.IndexByte(w.pending, '\n')
		if index < 0 {
			break
		}
		w.log.line(w.name, w.index, string(bytes.TrimSuffix(w.pending[:index], []byte{'\r'})))
		w.pending = w.pending[index+1:]
	}
	return len(data), nil
}
func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) > 0 {
		w.log.line(w.name, w.index, string(w.pending))
		w.pending = nil
	}
}
