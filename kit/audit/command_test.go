package audit_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/audit"
)

func TestCommandUsage(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		args []string
		want []string
	}{
		{"no action", nil, []string{"audit: no action was given", "Try: audit list"}},
		{"unknown action", []string{"show"}, []string{`unknown action "show"`, "list, export, prune"}},
		{"list resource", []string{"list", "--id", "1"}, []string{"--resource is required"}},
		{"list limit", []string{"list", "--resource", "t", "--limit", "1001"}, []string{"--limit 1001 is not from 1 to 1000", "audit export"}},
		{"list zero limit", []string{"list", "--resource", "t", "--limit", "0"}, []string{"--limit 0"}},
		{"list date", []string{"list", "--resource", "t", "--since", "yesterday"}, []string{`--since "yesterday" is not a date`, "YYYY-MM-DD"}},
		{"list argument", []string{"list", "--resource", "t", "extra"}, []string{`unexpected argument "extra"`}},
		{"list unknown flag", []string{"list", "--resource", "t", "--color"}, []string{"flag provided but not defined"}},
		{"list help", []string{"list", "-h"}, []string{"help was requested"}},
		{"export since", []string{"export", "--until", "2026-01-02", "--format", "csv"}, []string{"--since is required"}},
		{"export until", []string{"export", "--since", "2026-01-01", "--format", "csv"}, []string{"--until is required"}},
		{"export format", []string{"export", "--since", "2026-01-01", "--until", "2026-01-02"}, []string{"--format is required"}},
		{"export bad format", []string{"export", "--since", "2026-01-01", "--until", "2026-01-02", "--format", "xml"}, []string{`--format "xml" is not jsonl or csv`}},
		{"export range", []string{"export", "--since", "2026-01-02", "--until", "2026-01-01", "--format", "csv"}, []string{"--until is not after --since"}},
		{"prune before", []string{"prune", "--reason", "r", "--yes"}, []string{"--before is required"}},
		{"prune reason", []string{"prune", "--before", "2021-01-01", "--yes"}, []string{"--reason is required"}},
		{"prune yes", []string{"prune", "--before", "2021-01-01", "--reason", "r"}, []string{"--yes is required"}},
		{"no pool", []string{"list", "--resource", "t"}, []string{"the database pool is nil", "DATABASE_URL"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out, errOut bytes.Buffer
			err := audit.RunCommand(context.Background(), nil, tt.args, &out, &errOut)
			if err == nil {
				t.Fatal("no error")
			}
			msg := err.Error()
			if !strings.Contains(msg, "\nTry: ") {
				t.Errorf("error %q has no Try: line", msg)
			}
			for _, want := range tt.want {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q does not contain %q", msg, want)
				}
			}
			if out.Len() != 0 {
				t.Errorf("output = %q", out.String())
			}
		})
	}
}

func TestFormatEvent(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 3, 14, 40, 59, 0, time.FixedZone("x", 3600))
	for _, tt := range []struct {
		name string
		e    audit.Event
		all  bool
		want string
	}{
		{"update", audit.Event{OccurredAt: at, Action: "update", RecordID: "42",
			Changes: json.RawMessage(`{"status":{"old":"assigned","new":"done"}}`),
			Actor:   audit.Actor{Kind: "user", Email: "ben@example.com", Role: "staff"}},
			false, "2026-11-03 13:40  ben@example.com (staff)  update  status: assigned -> done"},
		{"create with record", audit.Event{OccurredAt: at, Action: "create", RecordID: "42",
			Changes: json.RawMessage(`{"title":{"new":"a b"},"done":{"new":false},"api_token":{"redacted":true}}`),
			Actor:   audit.Actor{Kind: "unattributed"}},
			true, `2026-11-03 13:40  unattributed  create #42  api_token: changed (value hidden), done: false, title: a b`},
		{"delete and truncated", audit.Event{OccurredAt: at, Action: "delete", RecordID: "7",
			Changes: json.RawMessage(`{"notes":{"old":{"truncated":true,"bytes":5000,"sha256":"ab"}},"title":{"old":""}}`),
			Actor:   audit.Actor{Kind: "system", Reason: "send_digest"}},
			false, `2026-11-03 13:40  system (send_digest)  delete  notes: (5000 bytes, truncated) -> (deleted), title: "" -> (deleted)`},
		{"control characters", audit.Event{OccurredAt: at, Action: "update", RecordID: "1",
			Changes: json.RawMessage(`{"ti\ntle":{"old":"a\u001b[31m","new":"b\r\nc"}}`),
			Actor:   audit.Actor{Kind: "operator", Email: "operator:kim\n"}},
			false, `2026-11-03 13:40  operator:kim\n  update  ti\ntle: "a\x1b[31m" -> "b\r\nc"`},
		{"prune", audit.Event{OccurredAt: at, Action: "audit.prune", RecordID: "*",
			Changes: json.RawMessage(`{"before":"2021-01-01T00:00:00Z","count":2}`),
			Actor:   audit.Actor{Kind: "operator", Email: "operator:kim"}},
			false, `2026-11-03 13:40  operator:kim  audit.prune  before: 2021-01-01T00:00:00Z, count: 2`},
		{"no changes", audit.Event{OccurredAt: at, Action: "auth.sign_out", RecordID: "1", Changes: json.RawMessage(`{}`),
			Actor: audit.Actor{Kind: "user", Email: "a@example.com"}},
			false, `2026-11-03 13:40  a@example.com  auth.sign_out`},
	} {
		if got := audit.FormatEvent(tt.e, tt.all); got != tt.want {
			t.Errorf("%s:\n got %s\nwant %s", tt.name, got, tt.want)
		}
	}
}

func TestCommandList(t *testing.T) {
	t.Parallel()
	pool, _ := auditDB(t)
	ctx := context.Background()
	for _, title := range []string{"assigned", "done"} {
		if err := audit.InTx(audit.WithActor(ctx, ana), pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE todos SET title = $1 WHERE id = 1`, title)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) (string, string) {
		t.Helper()
		var out, errOut bytes.Buffer
		if err := audit.RunCommand(ctx, pool, args, &out, &errOut); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out.String(), errOut.String()
	}

	out, _ := run("list", "--resource", "todos", "--id", "1", "--actor", "ana@example.com")
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	line := regexp.MustCompile(`^\d{4}-\d\d-\d\d \d\d:\d\d  ana@example\.com \(manager\)  update  title: (seed!|assigned) -> (assigned|done)$`)
	if len(lines) != 2 || !line.MatchString(lines[0]) || !line.MatchString(lines[1]) || !strings.HasSuffix(lines[0], "-> done") {
		t.Fatalf("list output:\n%s", out)
	}

	out, _ = run("list", "--resource", "todos", "--unattributed", "--limit", "1")
	if !strings.Contains(out, "  unattributed  update #1  title: seed -> seed!") || strings.Count(out, "\n") != 1 {
		t.Errorf("unattributed output:\n%s", out)
	}

	out, _ = run("list", "--resource", "todos", "--json", "--action", "update", "--since", "2020-01-01", "--until", "2999-01-01")
	var n int
	for sc := bufio.NewScanner(strings.NewReader(out)); sc.Scan(); n++ {
		var e audit.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil || e.Table != "todos" || e.Action != "update" {
			t.Fatalf("JSON line %q: %+v, %v", sc.Text(), e, err)
		}
	}
	if n != 4 {
		t.Errorf("JSON lines = %d, want 4", n)
	}

	out, errOut := run("list", "--resource", "nothing")
	if out != "" || !strings.Contains(errOut, "no events match") {
		t.Errorf("empty list: %q, %q", out, errOut)
	}
}

func TestCommandExport(t *testing.T) {
	t.Parallel()
	pool, _ := auditDB(t)
	ctx := context.Background()
	before := lastID(t, pool)
	total := 2*audit.ExportPage + 500
	exec(t, pool, `SELECT audit_append('export', 'things', g::text, NULL, jsonb_build_object('n', g), 'system', NULL, NULL, NULL, 'job', 'bulk', NULL)
		FROM generate_series(1, `+strconv.Itoa(total)+`) AS g`)
	all := int(lastID(t, pool))
	if all-int(before) != total {
		t.Fatalf("inserted %d events", all-int(before))
	}

	dir := t.TempDir()
	file := filepath.Join(dir, "audit.jsonl")
	var out, errOut bytes.Buffer
	args := []string{"export", "--since", "2000-01-01", "--until", "2999-01-01", "--format", "jsonl", "--out", file}
	if err := audit.RunCommand(ctx, pool, args, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	want := "exported " + strconv.Itoa(all) + " events\n" + hex.EncodeToString(sum[:]) + "  " + file + "\n"
	if out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, %v", info.Mode(), err)
	}
	var last int64
	var lines int
	for sc := bufio.NewScanner(bytes.NewReader(data)); sc.Scan(); lines++ {
		var e audit.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil || e.ID <= last {
			t.Fatalf("line %d: %+v, %v (ids must grow)", lines, e, err)
		}
		last = e.ID
	}
	if lines != all {
		t.Errorf("lines = %d, want %d", lines, all)
	}
	// An existing file is never replaced.
	if err := audit.RunCommand(ctx, pool, args, &out, &errOut); err == nil || !strings.Contains(err.Error(), "\nTry: pass a new file path") {
		t.Errorf("export to an existing file = %v", err)
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(after, data) {
		t.Error("the failed export changed the file")
	}

	// CSV to standard output: the digest goes to standard error.
	out.Reset()
	errOut.Reset()
	if err := audit.RunCommand(ctx, pool, []string{"export", "--since", "2000-01-01", "--until", "2999-01-01", "--format", "csv"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	sum = sha256.Sum256(out.Bytes())
	if errOut.String() != "exported "+strconv.Itoa(all)+" events\n"+hex.EncodeToString(sum[:])+"  -\n" {
		t.Errorf("summary = %q", errOut.String())
	}
	records, err := csv.NewReader(&out).ReadAll()
	if err != nil || len(records) != all+1 || records[0][0] != "id" || records[0][13] != "changes" {
		t.Fatalf("csv: %d records, %v", len(records), err)
	}
	lastRow := records[len(records)-1]
	if lastRow[2] != "export" || lastRow[3] != "things" || lastRow[6] != "system" || lastRow[11] != "bulk" || lastRow[13] != `{"n": `+strconv.Itoa(total)+`}` {
		t.Errorf("last row = %q", lastRow)
	}

	// A range with no events writes only the CSV header.
	out.Reset()
	errOut.Reset()
	if err := audit.RunCommand(ctx, pool, []string{"export", "--since", "2000-01-01", "--until", "2000-01-02", "--format", "csv"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "\n") != 1 || !strings.HasPrefix(errOut.String(), "exported 0 events\n") {
		t.Errorf("empty export: %q, %q", out.String(), errOut.String())
	}
}

func TestCommandPrune(t *testing.T) {
	pool, _ := auditDB(t)
	t.Setenv("SUDO_USER", "")
	t.Setenv("USER", "kim")
	exec(t, pool, `INSERT INTO audit_events (occurred_at, action, table_name, record_id, actor_kind, source)
		VALUES ('2020-01-01T00:00:00Z', 'auth.sign_in', 'users', '1', 'user', 'api')`)
	start := lastID(t, pool)
	var out, errOut bytes.Buffer
	if err := audit.RunCommand(context.Background(), pool, []string{"prune", "--before", "2021-01-01", "--reason", "retention", "--yes"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if out.String() != "pruned 1 events before 2021-01-01T00:00:00Z\n" {
		t.Errorf("output = %q", out.String())
	}
	got := events(t, pool, start)
	if len(got) != 1 || got[0].Email == nil || *got[0].Email != "operator:kim" || *got[0].Reason != "retention" {
		t.Fatalf("prune events = %+v", got)
	}
	wantEvent(t, got[0], "audit.prune", "audit_events", "*", "operator", "cli")

	err := audit.RunCommand(context.Background(), pool, []string{"prune", "--before", time.Now().UTC().Format("2006-01-02T15:04:05Z"), "--reason", "r", "--yes"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "younger than 1 day") || !strings.HasSuffix(err.Error(), "Try: pass a time at least 1 day in the past.") {
		t.Errorf("prune of a recent time = %v", err)
	}
}

// TestCommandExportFailureRemovesFile proves that a failed export leaves no
// partial file behind. The pool points at a closed port, so the first query fails.
func TestCommandExportFailureRemovesFile(t *testing.T) {
	t.Parallel()
	pool, err := pgxpool.New(t.Context(), "postgres://audit:audit@127.0.0.1:1/audit?connect_timeout=1&sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	file := filepath.Join(t.TempDir(), "events.jsonl")
	var out, errOut bytes.Buffer
	err = audit.RunCommand(t.Context(), pool, []string{"export", "--since", "2000-01-01", "--until", "2999-01-01", "--format", "jsonl", "--out", file}, &out, &errOut)
	if err == nil {
		t.Fatal("export against a closed port succeeded")
	}
	if _, statErr := os.Stat(file); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("the partial file stays after a failed export: %v", statErr)
	}
	if strings.Contains(out.String(), "exported") {
		t.Errorf("a failed export printed a summary: %s", out.String())
	}
}
