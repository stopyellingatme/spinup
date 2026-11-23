package console

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

var projectTime = time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)

func probeFS(values map[string]string) fstest.MapFS {
	files := fstest.MapFS{}
	for name, value := range values {
		files[name] = &fstest.MapFile{Data: []byte(value)}
	}
	return files
}

func runProject(t *testing.T, files fs.FS, cfg config) overview {
	t.Helper()
	v, err := project(files, cfg, projectTime)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func baseProbes() fstest.MapFS {
	return probeFS(map[string]string{
		"release.json": "null", "releases.json": "[]", "deploy.state": "", "upstream.caddy": "",
		"containers.ndjson": "", "jobs.json": "[]", "backup.json": "[]",
		"disk.txt":   "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/vda1 100 30 60 30% /opt/todo\n",
		"memory.txt": "MemTotal: 100 kB\nMemFree: 10 kB\nMemAvailable: 70 kB\n",
	})
}

func TestProjectComplete(t *testing.T) {
	files := baseProbes()
	files["containers.ndjson"].Data = []byte(`{"project":"todo-green","service":"api","name":"/z","state":"exited","health":null}
{"project":"unrelated","service":"db","name":"ignored","state":"running"}
{"project":"todo","service":"postgres","name":"/a","state":"running","health":"healthy"}
`)
	v := runProject(t, files, config{App: "todo", Env: "production", Profile: "none", Backend: "pgdump", Jobs: true})
	if v.Version != 1 || v.App != "todo" || v.Env != "production" || v.Profile != "none" || !v.ObservedAt.Equal(projectTime) {
		t.Fatalf("identity: %+v", v)
	}
	if v.Release.State != "ok" || v.Release.Data != nil || v.Backup.State != "ok" || v.Backup.Data != nil {
		t.Fatalf("known absence: release=%+v backup=%+v", v.Release, v.Backup)
	}
	if v.Jobs.Data == nil || *v.Jobs.Data == nil || len(*v.Jobs.Data) != 0 || v.History.Data == nil || *v.History.Data == nil {
		t.Fatal("empty success must encode []")
	}
	if v.Disk.Data == nil || *v.Disk.Data != (space{102400, 30720, 61440}) || v.Memory.Data == nil || *v.Memory.Data != (space{102400, 30720, 71680}) {
		t.Fatalf("space conversion: disk=%+v memory=%+v", v.Disk.Data, v.Memory.Data)
	}
	if v.Containers.Data == nil || len(*v.Containers.Data) != 2 || (*v.Containers.Data)[0].Name != "a" || (*v.Containers.Data)[1].Health != nil {
		t.Fatalf("container projection: %+v", v.Containers.Data)
	}
	if v.WAL.State != "disabled" || *v.WAL.Reason != "no_wal" || v.WAL.At != nil || *v.Alerts.Reason != "profile_none" {
		t.Fatal("feature selection was not applied")
	}
	first, _ := json.Marshal(v)
	second, _ := json.Marshal(runProject(t, files, config{App: "todo", Env: "production", Profile: "none", Backend: "pgdump", Jobs: true}))
	if string(first) != string(second) {
		t.Fatal("projection is not deterministic")
	}
}

func TestProjectPartial(t *testing.T) {
	files := baseProbes()
	files["disk.txt.err"] = &fstest.MapFile{Data: []byte("timeout\n")}
	files["jobs.json"].Data = []byte(`[{"queue":"default","state":"available","count":0}]`)
	files["memory.txt"].Data = []byte("MemTotal: 100 kB\nMemAvailable: 101 kB\n")
	v := runProject(t, files, config{App: "todo", Env: "production", Profile: "lite", Backend: "none", Jobs: true})
	if v.Disk.State != "unavailable" || *v.Disk.Reason != "timeout" || v.Disk.Data != nil || !v.Disk.At.Equal(projectTime) {
		t.Fatalf("probe error precedence: %+v", v.Disk)
	}
	if v.Memory.State != "unavailable" || *v.Memory.Reason != "invalid" || v.Containers.State != "ok" || v.Jobs.State != "ok" || len(*v.Jobs.Data) != 0 {
		t.Fatal("invalid source damaged an independent observation")
	}
	if v.Alerts.State != "unavailable" || *v.Alerts.Reason != "missing" || v.Alerts.At != nil || *v.Backup.Reason != "no_backups" {
		t.Fatal("unqueried alerts or disabled backups are incorrect")
	}
	missing := runProject(t, fstest.MapFS{}, config{App: "todo", Env: "production", Profile: "absent", Backend: "none"})
	if missing.Release.State != "unavailable" || *missing.Release.Reason != "missing" || *missing.Jobs.Reason != "no_jobs" || *missing.Alerts.Reason != "no_monitoring" {
		t.Fatal("missing inputs became healthy")
	}
}

const (
	currentID  = "20261122T170000Z-0123456789ab"
	olderID    = "20261121T170000Z-abcdef012345"
	failedID   = "20261122T173000Z-111111111111"
	resumedID  = "20261122T174000Z-222222222222"
	currentSHA = "0123456789abcdef0123456789abcdef01234567"
)

func releaseFixture(id, sha, color, created string) string {
	return `{"release":"` + id + `","app":"todo","env":"production","git_sha":"` + sha + `","color":"` + color + `","created_at":"` + created + `","user":"operator"}`
}

func eventFixture(id, step, status, at string) string {
	return `{"release":"` + id + `","step":"` + step + `","status":"` + status + `","at":"` + at + `","detail":"private journal prose"}` + "\n"
}

func TestProjectHistory(t *testing.T) {
	files := baseProbes()
	current := releaseFixture(currentID, currentSHA, "blue", "2026-11-22T17:00:00Z")
	older := releaseFixture(olderID, "abcdef012345abcdef012345abcdef012345abcd", "green", "2026-11-21T17:00:00Z")
	files["release.json"].Data = []byte(older) // Rollback restored an older release.
	files["upstream.caddy"].Data = []byte("reverse_proxy {\n name {args[0]}-green\n}\n")
	files["releases.json"].Data = []byte("[" + current + "," + older + "," + older + "]")
	files["deploy.state"].Data = []byte("truncated first record\n" +
		eventFixture(currentID, "post", "done", "2026-11-22T17:01:00Z") +
		eventFixture(failedID, "upload", "failed", "2026-11-22T17:30:00Z") +
		eventFixture(resumedID, "migrate", "failed", "2026-11-22T17:59:00Z") +
		eventFixture(resumedID, "post", "done", "2026-11-22T17:50:00Z") +
		eventFixture("rollback", "post", "done", "2026-11-22T17:55:00Z") +
		eventFixture(currentID, "post", "failed", "2026-11-22T17:59:00Z")[:40]) // Incomplete last line is ignored.
	files["snapshots/"+resumedID+".json"] = &fstest.MapFile{Data: []byte(releaseFixture(resumedID, "222222222222abcdef0123456789abcdef012345", "green", "2026-11-22T17:39:00Z"))}
	cfg := config{App: "todo", Env: "production", Profile: "absent", Backend: "none"}
	v := runProject(t, files, cfg)
	if v.Release.State != "ok" || v.Release.Data.AgeSeconds != 90000 || v.Release.Data.PreviousID != nil {
		t.Fatalf("rollback age or previous release: %+v", v.Release)
	}
	if v.History.Data == nil {
		t.Fatalf("history unavailable: %+v", v.History)
	}
	rows := *v.History.Data
	if len(rows) != 4 || rows[0].ID != resumedID || rows[0].Result != "succeeded" || rows[0].User == nil || *rows[0].User != "operator" || rows[0].At.Format(time.RFC3339) != "2026-11-22T17:39:00Z" {
		t.Fatalf("resume and metadata: %+v", rows)
	}
	if rows[1].ID != failedID || rows[1].Result != "failed" || rows[1].User != nil || rows[1].GitSHA != nil || rows[1].Color != nil {
		t.Fatalf("pre-record failure invented metadata: %+v", rows[1])
	}
	if rows[2].Result != "succeeded" || rows[3].ID != olderID || rows[3].Result != "recorded" {
		t.Fatalf("partial lines, rollback or retained rows: %+v", rows)
	}
	files["release.json"].Data = []byte(current)
	v = runProject(t, files, cfg)
	if v.Release.State != "unavailable" || *v.Release.Reason != "invalid" || v.History.State != "ok" {
		t.Fatal("Caddy disagreement was accepted")
	}
	files["upstream.caddy"].Data = []byte(" name {args[0]}-blue\n")
	v = runProject(t, files, cfg)
	if v.Release.Data == nil || v.Release.Data.PreviousID == nil || *v.Release.Data.PreviousID != olderID || v.Release.Data.AgeSeconds != 3600 {
		t.Fatalf("stored previous ID: %+v", v.Release)
	}
	files["releases.json.err"] = &fstest.MapFile{Data: []byte("source_down")}
	v = runProject(t, files, cfg)
	if v.Release.State != "ok" || v.Release.Data.PreviousID != nil || v.History.State != "unavailable" {
		t.Fatal("history outage erased the current release")
	}
}

func TestProjectBounds(t *testing.T) {
	t.Run("pgbackrest current database and repository", func(t *testing.T) {
		files := baseProbes()
		raw, err := os.ReadFile("testdata/probes/pgbackrest.json")
		if err != nil {
			t.Fatal(err)
		}
		files["backup.json"].Data = raw
		files["wal.json"] = &fstest.MapFile{Data: []byte(`{"archive_mode":"on","archived_at":"2026-11-22T17:00:00Z","failed_at":null}`)}
		cfg := config{App: "todo", Env: "production", Profile: "full", Backend: "pgbackrest"}
		v := runProject(t, files, cfg)
		if v.Backup.Data == nil || v.Backup.Data.Kind != "diff" || v.Backup.Data.SizeBytes == nil || *v.Backup.Data.SizeBytes != 31403056 || v.WAL.Data == nil || v.WAL.Data.MinSegment == nil || *v.WAL.Data.MinSegment != "000000010000000000000001" {
			t.Fatalf("native projection: backup=%+v wal=%+v", v.Backup, v.WAL)
		}
		var native []map[string]any
		if err := json.Unmarshal(raw, &native); err != nil {
			t.Fatal(err)
		}
		backups := native[0]["backup"].([]any)
		native[0]["backup"] = []any{backups[1], backups[0], map[string]any{"error": true, "label": "unfinished"}}
		raw, err = json.Marshal(native)
		if err != nil {
			t.Fatal(err)
		}
		files["backup.json"].Data = raw
		v = runProject(t, files, cfg)
		if v.Backup.Data == nil || v.Backup.Data.Kind != "diff" {
			t.Fatal("did not select newest successful backup by stop time")
		}
		native[0]["db"] = []any{map[string]any{"id": 2, "repo-key": 1}, map[string]any{"id": 1, "repo-key": 1}}
		raw, err = json.Marshal(native)
		if err != nil {
			t.Fatal(err)
		}
		files["backup.json"].Data = raw
		v = runProject(t, files, cfg)
		if v.WAL.Data == nil || v.WAL.Data.MinSegment != nil || v.WAL.Data.MaxSegment != nil {
			t.Fatal("used an older cluster's archive bounds")
		}
		native[0]["archive"] = []any{
			map[string]any{"database": map[string]any{"id": 2, "repo-key": 2}, "min": "000000010000000000000007", "max": "000000010000000000000009"},
			map[string]any{"database": map[string]any{"id": 1, "repo-key": 1}, "min": "000000010000000000000001", "max": "000000010000000000000005"},
		}
		raw, err = json.Marshal(native)
		if err != nil {
			t.Fatal(err)
		}
		files["backup.json"].Data = raw
		v = runProject(t, files, cfg)
		if v.WAL.Data.MinSegment != nil {
			t.Fatal("used a different repository's archive bounds")
		}
		native[0]["archive"] = []any{
			map[string]any{"database": map[string]any{"id": 2, "repo-key": 1}, "min": "000000010000000000000007", "max": "000000010000000000000009"},
			map[string]any{"database": map[string]any{"id": 1, "repo-key": 1}, "min": "000000010000000000000001", "max": "000000010000000000000005"},
		}
		raw, err = json.Marshal(native)
		if err != nil {
			t.Fatal(err)
		}
		files["backup.json"].Data = raw
		v = runProject(t, files, cfg)
		if v.WAL.Data.MinSegment == nil || *v.WAL.Data.MinSegment != "000000010000000000000007" {
			t.Fatal("did not select the matching database")
		}
		native[0]["archive"] = []any{map[string]any{"database": map[string]any{"id": 2, "repo-key": 1}, "min": nil, "max": "000000010000000000000009"}}
		raw, err = json.Marshal(native)
		if err != nil {
			t.Fatal(err)
		}
		files["backup.json"].Data = raw
		v = runProject(t, files, cfg)
		if v.WAL.Data.MinSegment != nil || v.WAL.Data.MaxSegment == nil || *v.WAL.Data.MaxSegment != "000000010000000000000009" {
			t.Fatal("missing optional bound erased its independent bound")
		}
		files["backup.json.err"] = &fstest.MapFile{Data: []byte("timeout")}
		v = runProject(t, files, cfg)
		if v.Backup.State != "unavailable" || *v.Backup.Reason != "timeout" || v.WAL.State != "ok" || v.WAL.Data.State != "archiving" || v.WAL.Data.MinSegment != nil {
			t.Fatal("repository failure erased independent SQL facts")
		}

		delete(files, "backup.json.err")
		native[0]["status"] = map[string]any{"code": 6}
		raw, err = json.Marshal(native)
		if err != nil {
			t.Fatal(err)
		}
		files["backup.json"].Data = raw
		v = runProject(t, files, cfg)
		if v.Backup.State != "unavailable" || *v.Backup.Reason != "source_down" || v.WAL.State != "ok" || v.WAL.Data.MinSegment != nil {
			t.Fatal("nonzero repository status became healthy")
		}
	})
	t.Run("input and row limits", func(t *testing.T) {
		for _, name := range []string{"disk.txt", "memory.txt", "jobs.json", "containers.ndjson", "release.json", "backup.json", "deploy.state", "releases.json", "wal.json"} {
			t.Run(name, func(t *testing.T) {
				files := baseProbes()
				files[name] = &fstest.MapFile{Data: []byte(strings.Repeat(" ", 2*1024*1024+1))}
				v := runProject(t, files, config{App: "todo", Env: "production", Profile: "full", Backend: "pgbackrest", Jobs: true})
				states := map[string]string{"disk.txt": v.Disk.State, "memory.txt": v.Memory.State, "jobs.json": v.Jobs.State, "containers.ndjson": v.Containers.State, "release.json": v.Release.State, "backup.json": v.Backup.State, "deploy.state": v.History.State, "releases.json": v.History.State, "wal.json": v.WAL.State}
				if states[name] != "unavailable" {
					t.Fatal("oversized input accepted")
				}
			})
		}
		for _, count := range []int{512, 513} {
			files := baseProbes()
			rows := []queue{}
			for i := 0; i < count; i++ {
				rows = append(rows, queue{Queue: fmt.Sprintf("q%03d", i), State: "available", Count: 1})
			}
			raw, err := json.Marshal(rows)
			if err != nil {
				t.Fatal(err)
			}
			files["jobs.json"].Data = raw
			files["containers.ndjson"].Data = []byte(strings.Repeat(`{"project":"todo","service":"api","name":"app","state":"exited"}`+"\n", count))
			v := runProject(t, files, config{App: "todo", Env: "production", Profile: "none", Backend: "none", Jobs: true})
			if count == 512 && (v.Jobs.State != "ok" || v.Containers.State != "ok") {
				t.Fatal("accepted limit was rejected")
			}
			if count == 513 && (v.Jobs.State != "unavailable" || v.Containers.State != "unavailable") {
				t.Fatal("row limit was not enforced")
			}
		}
	})
	t.Run("encoded JSON budget", func(t *testing.T) {
		files := baseProbes()
		// Escaping '<' expands each byte to six bytes in JSON v1 output.
		raw, _ := json.Marshal(container{Project: "todo", Service: "api", Name: strings.Repeat("<", 70000), State: "running"})
		files["containers.ndjson"].Data = raw
		v := runProject(t, files, config{App: "todo", Env: "production", Profile: "none", Backend: "none"})
		encoded, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if v.Containers.State != "unavailable" || *v.Containers.Reason != "invalid" || len(encoded) > 384*1024 || v.Disk.State != "ok" {
			t.Fatal("encoded byte budget was not enforced")
		}
	})
}

func TestProjectSecretProjection(t *testing.T) {
	files := baseProbes()
	malicious := strings.TrimSuffix(releaseFixture(currentID, currentSHA, "blue", "2026-11-22T17:00:00Z"), "}") + `,"secrets":{"DB_PASSWORD":"secret-hash-do-not-publish"},"images":[{"ref":"private.example/app@sha256:do-not-publish"}]}`
	files["release.json"].Data = []byte(malicious)
	files["releases.json"].Data = []byte("[" + malicious + "]")
	files["deploy.state"].Data = []byte(eventFixture(currentID, "post", "done", "2026-11-22T17:01:00Z"))
	files["upstream.caddy"].Data = []byte(" name {args[0]}-blue\n")
	v := runProject(t, files, config{App: "todo", Env: "production", Profile: "none", Backend: "none"})
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-hash-do-not-publish", "private journal prose", "private.example", "images", "secrets", "detail"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("projected forbidden field %q", secret)
		}
	}
	if v.Release.State != "ok" || v.History.State != "ok" {
		t.Fatal("extra private fields prevented readable projections")
	}
}

func TestProjectCompleteBackends(t *testing.T) {
	for _, backend := range []string{"pgbackrest", "walg", "pgdump"} {
		t.Run(backend, func(t *testing.T) {
			files := baseProbes()
			if backend == "pgdump" {
				files["backup.json"].Data = []byte(`[{"label":"20261122T170000ZF","finished_at":"2026-11-22T17:01:00Z","size_bytes":0},{"label":"20261121T170000ZF","finished_at":"2026-11-21T17:01:00Z"}]`)
			} else {
				raw, err := os.ReadFile("testdata/probes/" + backend + ".json")
				if err != nil {
					t.Fatal(err)
				}
				files["backup.json"].Data = raw
			}
			v := runProject(t, files, config{App: "todo", Env: "production", Profile: "none", Backend: backend})
			if v.Backup.Data == nil || v.Backup.Data.Backend != backend || v.Backup.Data.SizeBytes == nil {
				t.Fatalf("native %s metadata: %+v", backend, v.Backup)
			}
			if backend == "walg" && (v.Backup.Data.Kind != "incr" || *v.Backup.Data.SizeBytes != 2048) {
				t.Fatalf("WAL-G mapping: %+v", v.Backup.Data)
			}
			if backend == "pgdump" && *v.Backup.Data.SizeBytes != 0 {
				t.Fatal("zero size was lost")
			}
		})
	}
	t.Run("unknown size", func(t *testing.T) {
		files := baseProbes()
		files["backup.json"].Data = []byte(`[{"backup_name":"base_000000010000000000000001","time":"2026-11-22T17:00:00Z"}]`)
		v := runProject(t, files, config{App: "todo", Env: "production", Profile: "none", Backend: "walg"})
		if v.Backup.Data == nil || v.Backup.Data.SizeBytes != nil || v.Backup.Data.Kind != "full" {
			t.Fatal("unknown size became zero")
		}
	})
	t.Run("missing stanza", func(t *testing.T) {
		files := baseProbes()
		raw, err := os.ReadFile("testdata/probes/pgbackrest-missing.json")
		if err != nil {
			t.Fatal(err)
		}
		files["backup.json"].Data = raw
		v := runProject(t, files, config{App: "todo", Env: "production", Profile: "none"})
		if v.Backup.State != "ok" || v.Backup.Data != nil {
			t.Fatal("missing stanza is a known absence")
		}
	})
	t.Run("SQL state precedence", func(t *testing.T) {
		for _, tc := range []struct{ mode, archived, failed, want string }{
			{"off", "null", `"2026-11-22T17:00:00Z"`, "off"},
			{"on", "null", `"2026-11-22T17:00:00Z"`, "failing"},
			{"on", "null", "null", "waiting"},
			{"always", `"2026-11-22T17:00:00Z"`, `"2026-11-22T17:01:00Z"`, "failing"},
			{"on", `"2026-11-22T17:01:00Z"`, `"2026-11-22T17:00:00Z"`, "archiving"},
			{"on", `"2026-11-22T17:01:00Z"`, `"2026-11-22T17:01:00Z"`, "archiving"},
		} {
			files := baseProbes()
			files["wal.json"] = &fstest.MapFile{Data: []byte(`{"archive_mode":"` + tc.mode + `","archived_at":` + tc.archived + `,"failed_at":` + tc.failed + `}`)}
			v := runProject(t, files, config{App: "todo", Env: "production", Profile: "none", Backend: "pgbackrest"})
			if v.WAL.Data == nil || v.WAL.Data.State != tc.want {
				t.Fatalf("%+v produced %+v", tc, v.WAL)
			}
		}
	})
}

func TestProjectPartialInvalid(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"disk.txt", "Filesystem 1M-blocks Used Available Capacity Mounted on\n/dev/x 100 30 60 30% /opt/todo\n"},
		{"disk.txt", "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/x -1 0 0 0% /opt/todo\n"},
		{"disk.txt", "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/x 9007199254740992 0 0 0% /opt/todo\n"},
		{"disk.txt", "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/x 100 80 30 80% /opt/todo\n"},
		{"disk.txt", "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/x 100 1.5 30 80% /opt/todo\n"},
		{"memory.txt", "MemTotal: 100 kB\nMemAvailable: -1 kB\n"},
		{"memory.txt", "MemTotal: 100 MB\nMemAvailable: 10 MB\n"},
		{"memory.txt", "MemTotal: 100 kB\n"},
		{"memory.txt", "MemTotal: 100 kB\nMemTotal: 100 kB\nMemAvailable: 10 kB\n"},
		{"memory.txt", "MemTotal: 9223372036854775807 kB\nMemAvailable: 0 kB\n"},
		{"jobs.json", `[{"queue":"q","state":"available","count":-1}]`},
		{"jobs.json", `[{"queue":"q","state":"available","count":null}]`},
		{"jobs.json", `[{"queue":"q","state":"available","count":1.5}]`},
		{"jobs.json", `[{"queue":"` + strings.Repeat("é", 65) + `","state":"available","count":1}]`},
		{"jobs.json", `null`},
		{"containers.ndjson", `{"project":"todo","service":"api","name":"a"}`},
		{"backup.json", `[{"label":"20261122T170000ZF","finished_at":"2026-11-22T17:00:00Z","size_bytes":-1}]`},
		{"backup.json", `[{"label":"20261122T170000ZF","finished_at":"2026-11-22T18:00:06Z"}]`},
		{"release.json", releaseFixture(currentID, currentSHA, "blue", "2026-11-22T18:00:06Z")},
		{"release.json", strings.Replace(releaseFixture(currentID, currentSHA, "blue", "2026-11-22T17:00:00Z"), `"env":"production"`, `"env":"other"`, 1)},
	} {
		t.Run(tc.name+tc.value, func(t *testing.T) {
			files := baseProbes()
			files[tc.name] = &fstest.MapFile{Data: []byte(tc.value)}
			files["upstream.caddy"].Data = []byte("name {args[0]}-blue\n")
			v := runProject(t, files, config{App: "todo", Env: "production", Profile: "none", Backend: "pgdump", Jobs: true})
			states := map[string]string{"disk.txt": v.Disk.State, "memory.txt": v.Memory.State, "jobs.json": v.Jobs.State, "containers.ndjson": v.Containers.State, "backup.json": v.Backup.State, "release.json": v.Release.State}
			if states[tc.name] != "unavailable" || v.History.State != "ok" {
				t.Fatalf("invalid observation was accepted or damaged history: %+v", v)
			}
		})
	}
	t.Run("source diagnostics", func(t *testing.T) {
		for _, reason := range []string{"missing", "timeout", "invalid", "source_down", "password=private"} {
			files := baseProbes()
			files["disk.txt.err"] = &fstest.MapFile{Data: []byte(reason + "\n")}
			v := runProject(t, files, config{App: "todo", Env: "production", Profile: "none", Backend: "none"})
			want := reason
			if reason == "password=private" {
				want = "invalid"
			}
			if v.Disk.Data != nil || v.Disk.State != "unavailable" || *v.Disk.Reason != want {
				t.Fatal("invalid source error")
			}
		}
	})
	t.Run("zero counters and UTC", func(t *testing.T) {
		files := baseProbes()
		files["disk.txt"].Data = []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/x 0 0 0 0% /opt/todo\n")
		files["memory.txt"].Data = []byte("MemTotal: 0 kB\nMemAvailable: 0 kB\n")
		files["release.json"].Data = []byte(releaseFixture(currentID, currentSHA, "blue", "2026-11-22T19:00:05+01:00"))
		files["upstream.caddy"].Data = []byte("name {args[0]}-blue\n")
		v := runProject(t, files, config{App: "todo", Env: "production", Profile: "none", Backend: "none"})
		if v.Disk.Data == nil || v.Memory.Data == nil || v.Disk.Data.TotalBytes != 0 || v.Release.Data == nil || v.Release.Data.AgeSeconds != 0 || v.Release.Data.CreatedAt.Location() != time.UTC {
			t.Fatalf("zero or UTC projection: %+v", v)
		}
	})
}

func TestProjectHistoryLimits(t *testing.T) {
	t.Run("journal suffix and stable history limit", func(t *testing.T) {
		files := baseProbes()
		// This first valid event is outside the retained MiB and must disappear.
		journal := eventFixture(olderID, "upload", "failed", "2026-11-21T17:00:00Z")
		journal += strings.Repeat("x", 1024*1024) + "\n"
		for i := 24; i >= 0; i-- {
			id := fmt.Sprintf("20261122T170000Z-%012x", i)
			journal += eventFixture(id, "upload", "started", "2026-11-22T17:00:00Z")
		}
		files["deploy.state"].Data = []byte(journal)
		v := runProject(t, files, config{App: "todo", Env: "production", Profile: "none", Backend: "none"})
		if v.History.Data == nil || len(*v.History.Data) != 20 {
			t.Fatalf("history limit: %+v", v.History)
		}
		for i, row := range *v.History.Data {
			if row.ID != fmt.Sprintf("20261122T170000Z-%012x", i) || row.Result != "incomplete" || row.User != nil {
				t.Fatalf("history order or suffix: %+v", row)
			}
		}
	})
	t.Run("optional snapshot confinement", func(t *testing.T) {
		for _, name := range []string{"snapshots/" + failedID + ".json", "snapshots"} {
			files := baseProbes()
			files["deploy.state"].Data = []byte(eventFixture(failedID, "upload", "failed", "2026-11-22T17:30:00Z"))
			files[name] = &fstest.MapFile{Mode: fs.ModeSymlink, Data: []byte("/outside/private")}
			v := runProject(t, files, config{App: "todo", Env: "production", Profile: "none", Backend: "none"})
			if v.History.Data == nil || len(*v.History.Data) != 1 || (*v.History.Data)[0].User != nil {
				t.Fatal("followed a snapshot symlink")
			}
		}
		files := baseProbes()
		files["deploy.state"].Data = []byte(eventFixture(failedID, "upload", "failed", "2026-11-22T17:30:00Z"))
		files["snapshots/"+failedID+".json"] = &fstest.MapFile{Data: []byte(releaseFixture(currentID, currentSHA, "blue", "2026-11-22T17:00:00Z"))}
		v := runProject(t, files, config{App: "todo", Env: "production", Profile: "none", Backend: "none"})
		if (*v.History.Data)[0].GitSHA != nil {
			t.Fatal("accepted metadata for a different release")
		}
	})
	t.Run("empty and unknown metadata stay distinct", func(t *testing.T) {
		files := baseProbes()
		files["releases.json"].Data = []byte(`[{"release":"` + currentID + `","created_at":"2026-11-22T17:00:00Z","user":""},{"release":"` + olderID + `","created_at":"2026-11-21T17:00:00Z"}]`)
		v := runProject(t, files, config{App: "todo", Env: "production", Profile: "none", Backend: "none"})
		if v.History.Data == nil {
			t.Fatal("unknown metadata invalidated history")
		}
		rows := *v.History.Data
		if rows[0].User == nil || *rows[0].User != "" || rows[1].User != nil || rows[0].GitSHA != nil || rows[0].Color != nil {
			t.Fatal("null and empty metadata were conflated")
		}
	})
	t.Run("future journal event", func(t *testing.T) {
		files := baseProbes()
		files["deploy.state"].Data = []byte(eventFixture(failedID, "upload", "failed", "2026-11-22T18:00:06Z"))
		v := runProject(t, files, config{App: "todo", Env: "production", Profile: "none", Backend: "none"})
		if v.History.State != "unavailable" || *v.History.Reason != "invalid" || v.Release.State != "ok" {
			t.Fatal("future journal event accepted")
		}
	})
}

func TestProjectBoundsStringsAndOrder(t *testing.T) {
	files := baseProbes()
	files["jobs.json"].Data = []byte(`[{"queue":"é","state":"running","count":1},{"queue":"a","state":"running","count":2},{"queue":"a","state":"available","count":3},{"queue":"b\n","state":"available","count":4}]`)
	files["containers.ndjson"].Data = []byte(`{"project":"todo","service":"z","name":"/z","state":"exited"}
{"project":"todo","service":"a","name":"/b","state":"running"}
{"project":"todo","service":"a","name":"/a\t","state":"running","health":"healthy\u0000"}
{"project":"todo-console","service":"console","name":"/console","state":"running"}
{"project":"","service":"project","name":"temporary-projector","state":"running"}
`)
	v := runProject(t, files, config{App: "todo", Env: "production", Profile: "lite", Backend: "none", Jobs: true})
	if v.Jobs.Data == nil || (*v.Jobs.Data)[0].State != "available" || (*v.Jobs.Data)[2].Queue != "b" || (*v.Jobs.Data)[3].Queue != "é" {
		t.Fatalf("queue ordering or controls: %+v", v.Jobs)
	}
	if v.Containers.Data == nil || len(*v.Containers.Data) != 4 || (*v.Containers.Data)[0].Name != "a" || *(*v.Containers.Data)[0].Health != "healthy" || (*v.Containers.Data)[1].Name != "b" {
		t.Fatalf("container ordering, selection or controls: %+v", v.Containers)
	}
	for _, mode := range []fs.FileMode{fs.ModeSymlink, fs.ModeNamedPipe, fs.ModeDevice, fs.ModeDir} {
		files["memory.txt"] = &fstest.MapFile{Mode: mode, Data: []byte("ignored")}
		v = runProject(t, files, config{App: "todo", Env: "production", Profile: "none", Backend: "none"})
		if v.Memory.State != "unavailable" || *v.Memory.Reason != "invalid" {
			t.Fatal("non-regular input was accepted")
		}
	}
}

func TestProjectBoundsCombinedDocument(t *testing.T) {
	files := baseProbes()
	raw, err := json.Marshal(container{Project: "todo", Service: "api", Name: strings.Repeat("x", 200000), State: "running"})
	if err != nil {
		t.Fatal(err)
	}
	files["containers.ndjson"].Data = raw
	// Each section fits alone. Their combined encoding exceeds the host budget.
	files["backup.json"].Data = []byte(`[{"name":"todo","status":{"code":0},"backup":[{"label":"` + strings.Repeat("b", 200000) + `","type":"full","timestamp":{"stop":1795366800},"info":{"size":0}}]}]`)
	v := runProject(t, files, config{App: "todo", Env: "production", Profile: "none", Backend: "pgbackrest"})
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 384*1024 || (v.Backup.State != "unavailable" && v.Containers.State != "unavailable") || v.Memory.State != "ok" {
		t.Fatal("combined host budget was not enforced independently")
	}
}

func TestProjectPartialTimeAndSelection(t *testing.T) {
	cfg := config{App: "todo", Env: "production", Profile: "absent", Backend: "none"}
	if _, err := project(baseProbes(), cfg, time.Time{}); err == nil || !strings.Contains(err.Error(), "\nTry:") {
		t.Fatal("invalid sample time lacked a repair instruction")
	}
	files := baseProbes()
	for _, name := range []string{"backup.json.err", "wal.json.err", "jobs.json.err"} {
		files[name] = &fstest.MapFile{Data: []byte("source_down")}
	}
	v := runProject(t, files, cfg)
	if v.Backup.State != "disabled" || v.WAL.State != "disabled" || v.Jobs.State != "disabled" || v.Alerts.State != "disabled" {
		t.Fatal("probe errors overrode installed feature selection")
	}
}
