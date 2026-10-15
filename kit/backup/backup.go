package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"
)

// Path is the route of Handler.
const Path = "/_deploy/backup"

// InfoFunc returns the finish time of the newest backup. A zero time means no backup.
type InfoFunc func(ctx context.Context) (time.Time, error)

// ErrNoBackup reports that no backup exists yet.
var ErrNoBackup = errors.New("no backup exists yet")

// LastBackupAge returns the time since the newest backup, measured with time.Now.
// It returns ErrNoBackup when info returns a zero time.
func LastBackupAge(ctx context.Context, info InfoFunc) (time.Duration, error) {
	return age(ctx, info, time.Now)
}

func age(ctx context.Context, info InfoFunc, now func() time.Time) (time.Duration, error) {
	if info == nil {
		return 0, errors.New("read last backup age: the info function is nil\nTry: pass a function that returns the newest backup time")
	}
	last, err := info(ctx)
	if err != nil {
		return 0, fmt.Errorf("read last backup age: %w", err)
	}
	if last.IsZero() {
		return 0, ErrNoBackup
	}
	return max(now().Sub(last), 0), nil
}

// Response is the JSON body of Handler. LastBackupAgeSeconds is null when no backup exists.
type Response struct {
	LastBackupAgeSeconds *float64 `json:"last_backup_age_seconds"`
	Error                string   `json:"error,omitempty"`
}

// Handler serves the last backup age as JSON at Path. It answers 200 with the age in whole
// seconds, 200 with null when no backup exists, and 503 when info fails. The error text of
// info never reaches the response.
func Handler(info InfoFunc) http.Handler { return handler{info: info, now: time.Now} }

type handler struct {
	info InfoFunc
	now  func() time.Time
}

func (h handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body Response
	status := http.StatusOK
	d, err := age(r.Context(), h.info, h.now)
	switch {
	case err == nil:
		seconds := math.Floor(d.Seconds())
		body.LastBackupAgeSeconds = &seconds
	case errors.Is(err, ErrNoBackup):
	default:
		status = http.StatusServiceUnavailable
		body.Error = "backup state is unavailable"
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}
