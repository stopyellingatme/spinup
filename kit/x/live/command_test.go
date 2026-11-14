package live

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// run runs the command and returns its output.
func (h *testHub) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out strings.Builder
	err := h.runCommand(t.Context(), args, &out)
	return out.String(), err
}

func TestCommandFeeds(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	h := newHub(t, pool)
	out, err := h.run(t, "feeds", "--json")
	if err != nil || out != "[]\n" {
		t.Fatalf("empty feeds: %q %v", out, err)
	}
	publish(t, pool, "notes:7", Touch("1"))
	publish(t, pool, "notes:7", Touch("2"))
	publish(t, pool, "tasks", Touch("1"))
	c, _ := feedRow(t, pool, "notes:7")
	out, err = h.run(t, "feeds")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || strings.Join(strings.Fields(lines[0]), " ") != "KEY EPOCH SEQ PATCHES UPDATED" {
		t.Fatalf("table %q", out)
	}
	fields := strings.Fields(lines[1])
	if len(fields) != 5 || fields[0] != "notes:7" || fields[1] != itoa(c.Epoch) || fields[2] != "2" || fields[3] != "2" {
		t.Fatalf("row %q", lines[1])
	}
	if _, err := time.Parse(time.RFC3339, fields[4]); err != nil {
		t.Fatalf("updated %q: %v", fields[4], err)
	}
	if !strings.HasPrefix(lines[2], "tasks ") {
		t.Fatalf("row %q", lines[2])
	}
	out, err = h.run(t, "feeds", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var feeds []map[string]any
	if err := json.Unmarshal([]byte(out), &feeds); err != nil || len(feeds) != 2 {
		t.Fatalf("json %q %v", out, err)
	}
	for _, name := range []string{"key", "epoch", "seq", "patches", "updated"} {
		if _, ok := feeds[0][name]; !ok {
			t.Fatalf("json %q has no %s", out, name)
		}
	}
	if feeds[0]["key"] != "notes:7" || feeds[0]["patches"] != float64(2) || feeds[1]["key"] != "tasks" {
		t.Fatalf("json %q", out)
	}
}

func TestCommandPrune(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	h := newHub(t, pool)
	publish(t, pool, "notes", Touch("1"))
	publish(t, pool, "notes", Touch("2"))
	exec(t, pool, `UPDATE live_outbox SET created_at = now() - interval '3 hours' WHERE seq = 1`)
	out, err := h.run(t, "prune", "--older-than", "2h")
	if err != nil || out != "pruned 1 patches and 0 feeds\n" {
		t.Fatalf("prune: %q %v", out, err)
	}
	out, err = h.run(t, "prune")
	if err != nil || out != "pruned 0 patches and 0 feeds\n" {
		t.Fatalf("prune with the default age: %q %v", out, err)
	}
	exec(t, pool, `UPDATE live_outbox SET created_at = now() - interval '30 minutes'`)
	if out, err := h.run(t, "prune", "--older-than=10m"); err != nil || out != "pruned 1 patches and 0 feeds\n" {
		t.Fatalf("prune --older-than=10m: %q %v", out, err)
	}
}

func TestCommandReset(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	h := startHub(t, pool, WithPollInterval(50*time.Millisecond), WithHeartbeat(time.Hour))
	publish(t, pool, "notes", Touch("1"))
	publish(t, pool, "notes", Touch("2"))
	publish(t, pool, "tasks", Touch("1"))
	before, _ := feedRow(t, pool, "notes")

	server := httptest.NewServer(h.Stream(func(*http.Request) (Key, error) { return "notes", nil }))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	if !scanner.Scan() || scanner.Text() != "event: ready" {
		t.Fatalf("first line %q", scanner.Text())
	}

	out, err := h.run(t, "reset", "notes", "--yes")
	if err != nil || out != "reset notes\n" {
		t.Fatalf("reset: %q %v", out, err)
	}
	after, _ := feedRow(t, pool, "notes")
	if after.Seq != 0 || after.Epoch <= before.Epoch {
		t.Fatalf("after reset %+v, before %+v", after, before)
	}
	if n := count(t, pool, `SELECT count(*) FROM live_outbox WHERE feed = 'notes'`); n != 0 {
		t.Fatalf("%d outbox rows after reset", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM live_outbox WHERE feed = 'tasks'`); n != 1 {
		t.Fatal("reset of notes deleted the patches of tasks")
	}
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	text := strings.Join(lines, "\n")
	if !strings.Contains(text, "event: sync\ndata: {\"epoch\":"+itoa(after.Epoch)+",\"seq\":0,\"reason\":\"reset\"}") {
		t.Fatalf("stream after reset %q", text)
	}

	// The next publish starts the new epoch at seq 1.
	publish(t, pool, "notes", Touch("3"))
	if c, _ := feedRow(t, pool, "notes"); c.Seq != 1 || c.Epoch != after.Epoch {
		t.Fatalf("after publish %+v", c)
	}

	out, err = h.run(t, "reset", "--all", "--yes")
	if err != nil || out != "reset notes\nreset tasks\n" {
		t.Fatalf("reset --all: %q %v", out, err)
	}
	if n := count(t, pool, `SELECT count(*) FROM live_outbox`); n != 0 {
		t.Fatalf("%d outbox rows after reset --all", n)
	}
	if again, _ := feedRow(t, pool, "notes"); again.Epoch <= after.Epoch {
		t.Fatalf("a second reset kept the epoch %d", again.Epoch)
	}
}

func TestCommandErrors(t *testing.T) {
	t.Parallel()
	pool := liveDB(t)
	h := newHub(t, pool)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "live: no action was given."},
		{[]string{"drop"}, `live: the action "drop" is unknown.`},
		{[]string{"reset", "notes"}, "live reset: --yes is required, because the command deletes the patches of the feed."},
		{[]string{"reset", "--yes"}, "live reset: a KEY or --all is required."},
		{[]string{"reset", "notes", "--all", "--yes"}, "live reset: pass a KEY or --all, not both."},
		{[]string{"reset", "a", "b", "--yes"}, `live reset: the argument "b" is not expected.`},
		{[]string{"reset", "Bad", "--yes"}, `live reset: the key "Bad" is not valid.`},
		{[]string{"feeds", "--yes"}, `live feeds: the flag "--yes" is unknown.`},
		{[]string{"feeds", "extra"}, `live feeds: the argument "extra" is not expected.`},
		{[]string{"feeds", "--json=maybe"}, "live feeds: the flag --json takes no value."},
		{[]string{"prune", "--older-than"}, "live prune: the flag --older-than requires a duration, such as 1h."},
		{[]string{"prune", "--older-than", "soon"}, `live prune: the age "soon" is not a positive duration.`},
		{[]string{"prune", "--older-than", "-1h"}, `live prune: the age "-1h" is not a positive duration.`},
	} {
		_, err := h.run(t, tc.args...)
		if err == nil || !strings.HasPrefix(err.Error(), tc.want+"\nTry: "+commandUsage) {
			t.Errorf("%v: %v, want %q", tc.args, err, tc.want)
		}
	}
	_, err := h.run(t, "reset", "missing", "--yes")
	if err == nil || !strings.Contains(err.Error(), `live reset: the feed "missing" has no row.`) || !strings.Contains(err.Error(), "\nTry: ") {
		t.Fatalf("reset of a missing feed: %v", err)
	}
	if out, err := h.run(t, "reset", "--all", "--yes"); err != nil || out != "no feed to reset\n" {
		t.Fatalf("reset --all with no feed: %q %v", out, err)
	}
	if h.Command() == nil {
		t.Fatal("Command returned nil")
	}
	exec(t, pool, `DROP TABLE live_outbox`)
	_, err = h.run(t, "feeds")
	if err == nil || !strings.HasPrefix(err.Error(), "live feeds: ") || !strings.Contains(err.Error(), migrateTry) {
		t.Fatalf("feeds without the table: %v", err)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
