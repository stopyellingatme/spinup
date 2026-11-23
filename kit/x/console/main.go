package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"slices"
	"time"
)

type mainRuntime struct {
	getenv       func(string) string
	input, facts fs.FS
	client       *http.Client
	now          clock
	serve        func(*http.Server) error
}

// Main executes a console command and returns its process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	return runMain(args, stdout, stderr, mainRuntime{
		getenv: os.Getenv, input: os.DirFS("/input"), facts: os.DirFS("/data"),
		client: http.DefaultClient, now: time.Now,
		serve: func(server *http.Server) error { return server.ListenAndServe() },
	})
}

func runMain(args []string, stdout, stderr io.Writer, runtime mainRuntime) int {
	valid := len(args) == 1 && slices.Contains([]string{"serve", "query", "project", "healthcheck"}, args[0]) || len(args) == 2 && args[0] == "healthcheck" && args[1] == "query"
	fail := func(message string) int { _, _ = fmt.Fprintln(stderr, message); return 1 }
	if !valid {
		_, _ = fmt.Fprintln(stderr, "Cannot run console: the arguments are invalid.\nTry: use serve, query, project, healthcheck, or healthcheck query.")
		return 2
	}
	if args[0] == "healthcheck" {
		port := "7410"
		if len(args) == 2 {
			port = "7411"
		}
		if !processHealth(runtime.client, port) {
			return fail("Cannot check console health: the local process did not answer.\nTry: restart the console service.")
		}
		return 0
	}
	cfg, err := readConfig(runtime.getenv)
	if err != nil {
		return fail(err.Error())
	}
	if args[0] == "project" {
		at, err := time.Parse(time.RFC3339Nano, runtime.getenv("CONSOLE_OBSERVED_AT"))
		if err != nil || !validTime(at, runtime.now()) {
			return fail("Cannot project console facts: CONSOLE_OBSERVED_AT is invalid.\nTry: supply the UTC sample time.")
		}
		value, err := project(runtime.input, cfg, at)
		if err != nil {
			return fail(err.Error())
		}
		if err := json.NewEncoder(stdout).Encode(value); err != nil {
			return fail("Cannot write console facts: the output is unavailable.\nTry: check the projector output destination.")
		}
		return 0
	}
	address := ":7410"
	handler := webHandler(cfg, runtime.facts, runtime.client, runtime.now)
	if args[0] == "query" {
		address = ":7411"
		handler = queryHandler(runtime.client, runtime.now)
	}
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	if err := runtime.serve(server); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fail("Cannot serve console: the HTTP listener failed.\nTry: check the console service and its port binding.")
	}
	return 0
}

func processHealth(client *http.Client, port string) bool {
	client = queryClient(client)
	client.Timeout = 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/healthz", nil)
	if err != nil {
		return false
	}
	response, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4))
	return err == nil && response.StatusCode == http.StatusOK && string(data) == "ok\n"
}
