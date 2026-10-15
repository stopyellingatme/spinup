// Package project loads spinup configuration and resolves project environments.
package project

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	// ConfigFile is the project configuration file name.
	ConfigFile = "spinup.toml"
	// LockFile is the project lock file name.
	LockFile = "spinup.lock"
)

var (
	appNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	envNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

// Env defines a deployment environment.
type Env struct {
	Name      string
	Host      string
	User      string
	PublicURL string `toml:"public_url"`
	Protected bool
	Port      int
	// Transport moves images to the host: "ssh" (default, docker save | docker load) or
	// "registry" (push, then pull by digest). See ADR 0009 §4.
	Transport string
}

// Plugin defines a configured plugin.
type Plugin struct {
	Name    string
	Source  string
	Version string
}

// Config defines the spinup project configuration.
type Config struct {
	App struct {
		Name   string
		Module string
	} `toml:"app"`
	BehaviorVersion int      `toml:"behavior_version"`
	Modules         []string `toml:"modules"`
	Monitoring      struct {
		Profile      string
		HeartbeatURL string `toml:"heartbeat_url"`
		// AlertEmail receives Grafana email notifications.
		AlertEmail string `toml:"alert_email"`
		// SMTPHost identifies the SMTP relay address and port.
		SMTPHost string `toml:"smtp_host"`
		// SMTPUser identifies the SMTP relay user.
		SMTPUser string `toml:"smtp_user"`
		// SMTPFrom sets the sender of Grafana email notifications.
		SMTPFrom string `toml:"smtp_from"`
		// NtfyURL receives Grafana notifications through ntfy.
		NtfyURL string `toml:"ntfy_url"`
		// PrometheusRetention limits the age of stored metrics.
		PrometheusRetention string `toml:"prometheus_retention"`
		// PrometheusRetentionSize limits the disk space for stored metrics.
		PrometheusRetentionSize string `toml:"prometheus_retention_size"`
		// TempoRetention limits the age of stored traces.
		TempoRetention string `toml:"tempo_retention"`
	} `toml:"monitoring"`
	Backups struct {
		Retention string
		// Backend is "pgbackrest" (default) or "pgdump" (ADR 0011 §1).
		Backend string
		// ScheduleFull, ScheduleDiff, and ScheduleVerify are systemd OnCalendar values.
		ScheduleFull   string `toml:"schedule_full"`
		ScheduleDiff   string `toml:"schedule_diff"`
		ScheduleVerify string `toml:"schedule_verify"`
	} `toml:"backups"`
	Maintenance struct {
		// Window is the maintenance window in UTC, such as "Sun 02:00-05:00". Empty means any time.
		Window string
	} `toml:"maintenance"`
	Envs       map[string]Env `toml:"env"`
	Plugins    []Plugin       `toml:"plugins"`
	DefaultEnv string         `toml:"default_env"`
	Extra      map[string]any `toml:"-"`
}

// FindRoot finds the nearest parent directory containing spinup.toml.
func FindRoot(start string) (string, error) {
	path, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve project directory %q: %w", start, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("inspect project directory %q: %w", path, err)
	}
	if !info.IsDir() {
		path = filepath.Dir(path)
	}
	for {
		info, err := os.Stat(filepath.Join(path, ConfigFile))
		if err == nil && !info.IsDir() {
			return path, nil
		}
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}
	return "", fmt.Errorf("could not find %s from %q. Try: run this inside a spinup project, or pass --dir", ConfigFile, start)
}

// Load reads and parses a project configuration from root.
func Load(root string) (*Config, error) {
	data, err := os.ReadFile(filepath.Join(root, ConfigFile))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Join(root, ConfigFile), err)
	}
	return Parse(data)
}

// Parse decodes a spinup configuration document.
func Parse(data []byte) (*Config, error) {
	var config Config
	if err := toml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parse %s: %w", ConfigFile, err)
	}
	if config.Monitoring.Profile == "" {
		config.Monitoring.Profile = "lite"
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s extra keys: %w", ConfigFile, err)
	}
	for _, key := range []string{"app", "behavior_version", "modules", "monitoring", "backups", "maintenance", "env", "plugins", "default_env"} {
		delete(raw, key)
	}
	config.Extra = raw
	return &config, nil
}

// Validate checks that a project configuration is complete and valid.
func (c *Config) Validate() error {
	if c == nil {
		return errors.New("validate project configuration: configuration is nil")
	}
	var problems []error
	if !appNamePattern.MatchString(c.App.Name) {
		problems = append(problems, fmt.Errorf("app name %q must match %s", c.App.Name, appNamePattern.String()))
	}
	if c.App.Module == "" {
		problems = append(problems, errors.New("app module must not be empty"))
	}
	if c.Monitoring.Profile != "lite" && c.Monitoring.Profile != "full" && c.Monitoring.Profile != "none" {
		problems = append(problems, fmt.Errorf("monitoring profile %q must be lite, full, or none", c.Monitoring.Profile))
	}
	if b := c.Backups.Backend; b != "" && b != "pgbackrest" && b != "pgdump" && b != "walg" {
		problems = append(problems, fmt.Errorf("backups backend %q must be pgbackrest, pgdump, or walg", b))
	}
	envNames := make([]string, 0, len(c.Envs))
	for name := range c.Envs {
		envNames = append(envNames, name)
	}
	sort.Strings(envNames)
	for _, name := range envNames {
		if !envNamePattern.MatchString(name) {
			problems = append(problems, fmt.Errorf("environment name %q must match %s", name, envNamePattern.String()))
		}
		if t := c.Envs[name].Transport; t != "" && t != "ssh" && t != "registry" {
			problems = append(problems, fmt.Errorf("environment %q: transport %q must be ssh or registry", name, t))
		}
	}
	if c.DefaultEnv != "" {
		if _, ok := c.Envs[c.DefaultEnv]; !ok {
			problems = append(problems, fmt.Errorf("default environment %q does not exist", c.DefaultEnv))
		}
	}
	return errors.Join(problems...)
}

// ResolveEnv resolves an explicit or implied environment.
func (c *Config) ResolveEnv(name string) (Env, error) {
	if c == nil {
		return Env{}, errors.New("resolve environment: configuration is nil")
	}
	if name == "" {
		name = c.DefaultEnv
		if name == "" && len(c.Envs) == 1 {
			for name = range c.Envs {
			}
		}
	}
	if env, ok := c.Envs[name]; ok && name != "" {
		env.Name = name
		return env, nil
	}
	names := make([]string, 0, len(c.Envs))
	for envName := range c.Envs {
		names = append(names, envName)
	}
	sort.Strings(names)
	return Env{}, fmt.Errorf("could not resolve environment %q. Valid environments: %s. Try: pass --env <name>", name, strings.Join(names, ", "))
}

// Confirm controls protected-environment confirmations.
type Confirm struct {
	In          io.Reader
	Out         io.Writer
	Interactive bool
	Yes         bool
	CI          bool
}

// ConfirmProtected confirms an action that targets a protected environment.
func ConfirmProtected(env Env, action string, c Confirm) error {
	if !env.Protected || (c.Yes && c.CI) {
		return nil
	}
	if !c.Interactive {
		return fmt.Errorf("cannot %s in protected environment %q. Try: re-run in a terminal, or pass --yes in CI", action, env.Name)
	}
	if c.In == nil {
		return errors.New("read confirmation: input is nil")
	}
	if c.Out == nil {
		return errors.New("write confirmation: output is nil")
	}
	if _, err := fmt.Fprintf(c.Out, `%s targets the protected environment %q (%s). Type the environment name to continue:`, action, env.Name, env.Host); err != nil {
		return fmt.Errorf("write confirmation: %w", err)
	}
	answer, err := bufio.NewReader(c.In).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read confirmation: %w", err)
	}
	answer = strings.TrimSuffix(answer, "\n")
	answer = strings.TrimSuffix(answer, "\r")
	if answer != env.Name {
		return errors.New("confirmation did not match")
	}
	return nil
}
