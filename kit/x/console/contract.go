// Package console provides the SSH-accessible operator console.
package console

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

type config struct {
	App, Env, Profile, Backend, Origin string
	Jobs                               bool
}

type clock func() time.Time

type overview struct {
	Version    int                  `json:"version"`
	App        string               `json:"app"`
	Env        string               `json:"env"`
	Profile    string               `json:"profile"`
	ObservedAt *time.Time           `json:"observed_at"`
	Release    section[release]     `json:"release"`
	Containers section[[]container] `json:"containers"`
	Alerts     section[[]alert]     `json:"alerts"`
	Backup     section[backup]      `json:"backup"`
	WAL        section[wal]         `json:"wal"`
	Jobs       section[[]queue]     `json:"jobs"`
	Disk       section[space]       `json:"disk"`
	Memory     section[space]       `json:"memory"`
	History    section[[]attempt]   `json:"history"`
}

// section distinguishes an empty successful payload from an unavailable payload.
// Successful arrays use a pointer to a non-nil slice, including an empty slice.
type section[T any] struct {
	State  string     `json:"state"`
	Reason *string    `json:"reason"`
	At     *time.Time `json:"at"`
	Data   *T         `json:"data"`
}

type release struct {
	ID         string    `json:"id"`
	GitSHA     string    `json:"git_sha"`
	Color      string    `json:"color"`
	CreatedAt  time.Time `json:"created_at"`
	AgeSeconds int64     `json:"age_seconds"`
	PreviousID *string   `json:"previous_id"`
}

type container struct {
	Project string  `json:"project"`
	Service string  `json:"service"`
	Name    string  `json:"name"`
	State   string  `json:"state"`
	Health  *string `json:"health"`
}

type alert struct {
	Name     string     `json:"name"`
	Severity *string    `json:"severity"`
	Since    *time.Time `json:"since"`
}

type backup struct {
	Backend    string    `json:"backend"`
	Label      string    `json:"label"`
	Kind       string    `json:"kind"`
	FinishedAt time.Time `json:"finished_at"`
	SizeBytes  *int64    `json:"size_bytes"`
}

type wal struct {
	State      string     `json:"state"`
	ArchivedAt *time.Time `json:"archived_at"`
	FailedAt   *time.Time `json:"failed_at"`
	MinSegment *string    `json:"min_segment"`
	MaxSegment *string    `json:"max_segment"`
}

type queue struct {
	Queue string `json:"queue"`
	State string `json:"state"`
	Count int64  `json:"count"`
}

type space struct {
	TotalBytes     int64 `json:"total_bytes"`
	UsedBytes      int64 `json:"used_bytes"`
	AvailableBytes int64 `json:"available_bytes"`
}

type attempt struct {
	ID     string    `json:"id"`
	At     time.Time `json:"at"`
	User   *string   `json:"user"`
	GitSHA *string   `json:"git_sha"`
	Color  *string   `json:"color"`
	Result string    `json:"result"`
}

type logs struct {
	Version     int       `json:"version"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	Query       string    `json:"query"`
	Entries     []line    `json:"entries"`
	More        bool      `json:"more"`
	ExplorePath string    `json:"explore_path"`
}

type line struct {
	At      time.Time `json:"at"`
	Service string    `json:"service"`
	Level   *string   `json:"level"`
	Line    string    `json:"line"`
}

type wireError struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Try     string `json:"try"`
}

var errorCatalog = map[string]struct {
	Status       int
	Message, Try string
}{
	"invalid_filter":      {http.StatusBadRequest, "Invalid console query.", "Use the filters shown on the Logs page."},
	"forbidden":           {http.StatusForbidden, "Console origin refused.", "Open the URL printed by spinup console open."},
	"not_found":           {http.StatusNotFound, "Console route not found.", "Open the console overview."},
	"method_not_allowed":  {http.StatusMethodNotAllowed, "This console route accepts GET only.", "Open the console overview."},
	"monitoring_disabled": {http.StatusConflict, "Log search requires monitoring.", "Enable the lite or full monitoring profile and deploy."},
	"query_too_long":      {http.StatusRequestURITooLong, "Console query exceeds 8 KiB.", "Shorten the search text."},
	"busy":                {http.StatusTooManyRequests, "The console query limit is busy.", "Retry the query."},
	"source_down":         {http.StatusServiceUnavailable, "The log source did not answer with valid data.", "Check spinup monitor status."},
}

var (
	appName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	envName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

// readConfig requires the three identity variables. Page variables are optional
// for query mode, but their values must be valid when present.
func readConfig(getenv func(string) string) (config, error) {
	cfg := config{
		App: getenv("CONSOLE_APP"), Env: getenv("CONSOLE_ENV"), Profile: getenv("CONSOLE_PROFILE"),
		Backend: getenv("CONSOLE_BACKEND"), Origin: getenv("CONSOLE_ORIGIN"),
	}
	jobs := getenv("CONSOLE_JOBS")
	invalid := func(key string) (config, error) {
		return config{}, fmt.Errorf("cannot read console configuration: %s is invalid\nTry: deploy the console with valid CONSOLE_* values", key)
	}
	for _, item := range []struct{ key, value string }{
		{"CONSOLE_APP", cfg.App}, {"CONSOLE_ENV", cfg.Env}, {"CONSOLE_PROFILE", cfg.Profile},
		{"CONSOLE_BACKEND", cfg.Backend}, {"CONSOLE_ORIGIN", cfg.Origin}, {"CONSOLE_JOBS", jobs},
	} {
		if strings.ContainsAny(item.value, "\r\n\x00") {
			return invalid(item.key)
		}
	}
	if !appName.MatchString(cfg.App) {
		return invalid("CONSOLE_APP")
	}
	if !envName.MatchString(cfg.Env) {
		return invalid("CONSOLE_ENV")
	}
	if !slices.Contains([]string{"absent", "none", "lite", "full"}, cfg.Profile) {
		return invalid("CONSOLE_PROFILE")
	}
	if cfg.Backend != "" && !slices.Contains([]string{"none", "pgbackrest", "pgdump", "walg"}, cfg.Backend) {
		return invalid("CONSOLE_BACKEND")
	}
	if jobs != "" && jobs != "true" && jobs != "false" {
		return invalid("CONSOLE_JOBS")
	}
	cfg.Jobs = jobs == "true"
	if cfg.Origin != "" {
		u, err := url.Parse(cfg.Origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil ||
			(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || strings.Contains(cfg.Origin, "#") {
			return invalid("CONSOLE_ORIGIN")
		}
	}
	return cfg, nil
}
