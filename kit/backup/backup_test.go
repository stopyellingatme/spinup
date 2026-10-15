package backup

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLastBackupAge(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	for _, tt := range []struct {
		name    string
		info    InfoFunc
		want    time.Duration
		wantErr error
		anyErr  bool
	}{
		{"age", func(context.Context) (time.Time, error) { return now.Add(-90 * time.Second), nil }, 90 * time.Second, nil, false},
		{"future clamps to zero", func(context.Context) (time.Time, error) { return now.Add(time.Minute), nil }, 0, nil, false},
		{"no backup", func(context.Context) (time.Time, error) { return time.Time{}, nil }, 0, ErrNoBackup, true},
		{"info error", func(context.Context) (time.Time, error) { return time.Time{}, errors.New("boom") }, 0, nil, true},
		{"nil info", nil, 0, nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := age(context.Background(), tt.info, clock)
			if (err != nil) != tt.anyErr || (tt.wantErr != nil && !errors.Is(err, tt.wantErr)) {
				t.Fatalf("err = %v", err)
			}
			if got != tt.want {
				t.Fatalf("age = %v, want %v", got, tt.want)
			}
		})
	}
	if _, err := LastBackupAge(context.Background(), func(context.Context) (time.Time, error) { return time.Now().Add(-time.Hour), nil }); err != nil {
		t.Fatal(err)
	}
}

func TestHandler(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name, method string
		info         InfoFunc
		code         int
		body         string
	}{
		{"age", http.MethodGet, func(context.Context) (time.Time, error) { return now.Add(-3661500 * time.Millisecond), nil }, 200, `{"last_backup_age_seconds":3661}` + "\n"},
		{"no backup", http.MethodGet, func(context.Context) (time.Time, error) { return time.Time{}, nil }, 200, `{"last_backup_age_seconds":null}` + "\n"},
		{"error hides detail", http.MethodGet, func(context.Context) (time.Time, error) { return time.Time{}, errors.New("secret detail") }, 503, `{"last_backup_age_seconds":null,"error":"backup state is unavailable"}` + "\n"},
		{"head", http.MethodHead, func(context.Context) (time.Time, error) { return now, nil }, 200, ""},
		{"post", http.MethodPost, func(context.Context) (time.Time, error) { return now, nil }, 405, "method not allowed\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := handler{info: tt.info, now: func() time.Time { return now }}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), tt.method, Path, nil))
			if rec.Code != tt.code || rec.Body.String() != tt.body {
				t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
			}
			if tt.code != 405 && !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("content type %q", rec.Header().Get("Content-Type"))
			}
		})
	}
	if Handler(nil) == nil {
		t.Fatal("Handler returned nil")
	}
}
