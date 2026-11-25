//go:build unix

package consoleinstall

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type collectorCall struct {
	Args  []string
	Stdin string
}
type collectorFixture struct {
	t                                 *testing.T
	root, dir, bin, records, captured string
	env                               []string
}

func newCollector(t *testing.T) *collectorFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &collectorFixture{t: t, root: root, dir: filepath.Join(root, "console"), bin: filepath.Join(root, "bin"), records: filepath.Join(root, "records"), captured: filepath.Join(root, "captured")}
	for _, dir := range []string{f.dir, f.bin, f.records, f.captured, filepath.Join(root, "current"), filepath.Join(root, "caddy"), filepath.Join(root, "releases")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	source, err := os.ReadFile("collect.sh")
	if err != nil {
		t.Fatal(err)
	}
	// Only the isolated copy uses recording utilities. Production ignores the caller's PATH.
	source = []byte(strings.Replace(string(source), "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "PATH="+f.bin+":/usr/bin:/bin:/opt/homebrew/bin", 1))
	f.write("console/collect.sh", string(source), 0700)
	probe, err := os.ReadFile("testdata/probe-bin/probe")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"docker", "df", "id", "date", "stat", "flock", "mv", "timeout"} {
		if err := os.WriteFile(filepath.Join(f.bin, name), probe, 0700); err != nil {
			t.Fatal(err)
		}
	}
	memory, err := os.ReadFile("testdata/probe-bin/cat")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.bin, "cat"), memory, 0700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	timeout, err := exec.LookPath("timeout")
	if err != nil {
		t.Fatal("blocked: timeout")
	}
	f.env = append(os.Environ(), "GORACE=atexit_sleep_ms=0", "COLLECTOR_TEST_BINARY="+binary, "COLLECTOR_RECORDS="+f.records, "COLLECTOR_CAPTURED="+f.captured, "COLLECTOR_TIMEOUT="+timeout, "PYTHONPATH="+f.root)
	f.write("re.py", `raise RuntimeError("untrusted Python path")`, 0600)
	f.configure("pgbackrest", "true")
	f.write("current/release.json", `{"release":"20261122T180000Z-0123456789ab"}`, 0600)
	f.write("releases.json", "[]", 0600)
	f.write("caddy/upstream.caddy", "reverse_proxy api-blue:8080\n", 0600)
	f.write("deploy.state", "first complete\nsecond complete\npartial", 0600)
	return f
}
func (f *collectorFixture) write(name, data string, mode os.FileMode) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.root, name), []byte(data), mode); err != nil {
		f.t.Fatal(err)
	}
}
func (f *collectorFixture) configure(backend, jobs string) {
	f.write("console/probe.conf", "sample\nproduction\nlite\n"+backend+"\n"+jobs+"\nregistry.example/sample:release\nhttps://sample.example\n", 0600)
}
func (f *collectorFixture) run(args ...string) ([]byte, error) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", append([]string{filepath.Join(f.dir, "collect.sh")}, args...)...)
	cmd.Dir = "/"
	cmd.Env = f.env
	return cmd.CombinedOutput()
}
func (f *collectorFixture) calls() []collectorCall {
	f.t.Helper()
	var calls []collectorCall
	entries, err := os.ReadDir(f.records)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(f.records, e.Name()))
		if err != nil {
			f.t.Fatal(err)
		}
		var c collectorCall
		if err = json.Unmarshal(data, &c); err != nil {
			f.t.Fatal(err)
		}
		calls = append(calls, c)
	}
	return calls
}
func (f *collectorFixture) input(name string) string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.captured, name))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}
func requireCollector(t *testing.T, f *collectorFixture) {
	t.Helper()
	if out, err := f.run(); err != nil {
		t.Fatalf("collector: %v\n%s", err, out)
	}
}

func TestCollectorCommands(t *testing.T) {
	f := newCollector(t)
	requireCollector(t, f)
	calls := f.calls()
	var filters []string
	var sql []string
	var projector []string
	deadlines := map[string]int{}
	for _, c := range calls {
		if c.Args[0] == "timeout" {
			if len(c.Args) < 5 || c.Args[1] != "-k" || c.Args[2] != "1" {
				t.Fatalf("unsafe deadline: %q", c.Args)
			}
			deadlines[c.Args[3]]++
			continue
		}
		if c.Args[0] != "docker" {
			continue
		}
		a := c.Args[1:]
		switch a[0] {
		case "ps":
			var expected []string
			if slices.Contains(a, "label=com.docker.compose.service=postgres") {
				expected = []string{"ps", "--no-trunc", "--quiet", "--filter", "label=com.docker.compose.project=sample", "--filter", "label=com.docker.compose.service=postgres", "--filter", "status=running"}
			} else {
				if len(a) != 6 {
					t.Fatalf("unexpected project query: %q", a)
				}
				expected = []string{"ps", "-a", "--no-trunc", "--quiet", "--filter", a[5]}
				// The only variable is the exact project label.
				if !slices.Contains([]string{"label=com.docker.compose.project=sample", "label=com.docker.compose.project=sample-blue", "label=com.docker.compose.project=sample-green", "label=com.docker.compose.project=sample-monitoring", "label=com.docker.compose.project=sample-console"}, a[5]) {
					t.Fatalf("unexpected label: %q", a)
				}
			}
			if !slices.Equal(a, expected) {
				t.Fatalf("Docker query = %q, want %q", a, expected)
			}
			filters = append(filters, strings.Join(a, " "))
		case "inspect":
			format := `{"project":{{json (index .Config.Labels "com.docker.compose.project")}},"service":{{json (index .Config.Labels "com.docker.compose.service")}},"name":{{json (slice .Name 1)}},"state":{{json .State.Status}},"health":{{if .State.Health}}{{json .State.Health.Status}}{{else}}null{{end}}}`
			expected := []string{"inspect", "--type", "container", "--format", format, strings.Repeat("a", 64)}
			if !slices.Equal(a, expected) {
				t.Fatalf("unsafe inspect: %q", a)
			}
		case "exec":
			if slices.Contains(a, "psql") {
				sql = append(sql, c.Stdin)
				expected := []string{"exec", "-i", "--env", "PGOPTIONS=-c default_transaction_read_only=on -c statement_timeout=2000", strings.Repeat("b", 64), "psql", "-X", "-w", "-U", "app", "-d", "app", "-A", "-t", "-v", "ON_ERROR_STOP=1"}
				if strings.Contains(c.Stdin, "river_job") {
					expected = append(expected, "-v", "VERBOSITY=sqlstate")
				}
				if !slices.Equal(a, expected) {
					t.Fatalf("unsafe SQL command: %q", a)
				}
			} else {
				expected := []string{"exec", strings.Repeat("b", 64), "sh", "-c", `for f in /run/secrets/PGBACKREST_*; do [ -f "$f" ] && export "${f##*/}=$(cat "$f")"; done; exec pgbackrest "$@"`, "pgbackrest", "--stanza=sample", "--output=json", "info"}
				if !slices.Equal(a, expected) {
					t.Fatalf("unexpected repository probe: %q", a)
				}
			}
		case "run":
			projector = a
		case "rm":
			if len(a) != 4 || !slices.Equal(a[:3], []string{"rm", "-f", "--"}) || !strings.HasPrefix(a[3], "sample-console-project-.probe.") {
				t.Fatalf("unexpected cleanup: %q", a)
			}
		default:
			t.Fatalf("unexpected Docker command: %q", a)
		}
	}
	if deadlines["8"] != 1 || deadlines["3"] != 13 || len(deadlines) != 2 {
		t.Fatalf("probe deadlines: %v", deadlines)
	}
	for _, project := range []string{"sample", "sample-blue", "sample-green", "sample-monitoring", "sample-console"} {
		found := false
		for _, filter := range filters {
			if strings.Contains(filter+" ", "label=com.docker.compose.project="+project+" ") && !strings.Contains(filter, "service=") {
				found = true
			}
		}
		if !found {
			t.Errorf("missing exact project filter %s: %q", project, filters)
		}
	}
	base := false
	for _, filter := range filters {
		if strings.Contains(filter, "label=com.docker.compose.project=sample ") && strings.Contains(filter, "label=com.docker.compose.service=postgres") && strings.Contains(filter, "status=running") {
			base = true
		}
	}
	if !base {
		t.Fatal("missing base postgres filters")
	}
	if len(sql) != 2 {
		t.Fatalf("SQL calls = %d: %q", len(sql), sql)
	}
	expectedSQL := []string{
		`SELECT COALESCE(json_agg(json_build_object('queue', queue, 'state', state, 'count', count)
ORDER BY queue COLLATE "C", state COLLATE "C"), '[]'::json)
FROM (SELECT queue, state::text, count(*) FROM public.river_job
GROUP BY queue, state ORDER BY queue COLLATE "C", state::text COLLATE "C"
LIMIT 513) AS queues;
`,
		`SELECT json_build_object('archive_mode', current_setting('archive_mode'),
'archived_at', last_archived_time, 'failed_at', last_failed_time)
FROM pg_catalog.pg_stat_archiver;
`,
	}
	slices.Sort(sql)
	slices.Sort(expectedSQL)
	if !slices.Equal(sql, expectedSQL) {
		t.Fatalf("SQL changed:\ngot %q\nwant %q", sql, expectedSQL)
	}
	mountIndex := slices.Index(projector, "--mount")
	if mountIndex < 0 || mountIndex+1 >= len(projector) {
		t.Fatalf("missing input mount: %q", projector)
	}
	mount := projector[mountIndex+1]
	private := strings.TrimSuffix(strings.TrimPrefix(mount, "type=bind,src="), ",dst=/input,readonly")
	if !strings.HasPrefix(private, f.dir+"/.probe.") || filepath.Base(private) != "input" {
		t.Fatalf("unexpected mount: %q", mount)
	}
	projector[mountIndex+1] = "type=bind,src=<private>,dst=/input,readonly"
	nameIndex := slices.Index(projector, "--name")
	if nameIndex < 0 || nameIndex+1 >= len(projector) || projector[nameIndex+1] != "sample-console-project-"+filepath.Base(filepath.Dir(private)) {
		t.Fatalf("unexpected projector name: %q", projector)
	}
	projector[nameIndex+1] = "<private-name>"
	expectedProjector := []string{"run", "--rm", "--name", "<private-name>", "--pull", "never", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--pids-limit", "32", "--cpus", "0.25", "--memory", "48m", "--log-driver", "none", "--user", "1000:1000", "--env", "GOMEMLIMIT=24MiB", "--env", "CONSOLE_APP=sample", "--env", "CONSOLE_ENV=production", "--env", "CONSOLE_PROFILE=lite", "--env", "CONSOLE_BACKEND=pgbackrest", "--env", "CONSOLE_JOBS=true", "--env", "CONSOLE_ORIGIN=https://sample.example", "--env", "CONSOLE_OBSERVED_AT=2026-11-22T18:00:00Z", "--mount", "type=bind,src=<private>,dst=/input,readonly", "--entrypoint", "/console", "registry.example/sample:release", "project"}
	if !slices.Equal(projector, expectedProjector) {
		t.Fatalf("projector command = %q, want %q", projector, expectedProjector)
	}
	if got := f.input("memory.txt"); got != "MemTotal: 1000 kB\nMemAvailable: 400 kB\n" {
		t.Fatalf("memory source = %q", got)
	}
	if got := f.input("deploy.state"); got != "first complete\nsecond complete\n" {
		t.Fatalf("journal: %q", got)
	}
	if got := f.input("release.json"); !strings.Contains(got, "20261122T180000Z") {
		t.Fatal(got)
	}
}

// TestCollectorUtility implements recording host executables in a separate process.
func TestCollectorUtility(t *testing.T) {
	if os.Getenv("COLLECTOR_RECORDS") == "" {
		return
	}
	index := slices.Index(os.Args, "--")
	if index < 0 {
		os.Exit(2)
	}
	args := os.Args[index+1:]
	name := args[0]
	a := args[1:]
	var stdin []byte
	if name == "docker" && len(a) > 0 && a[0] == "exec" && slices.Contains(a, "psql") {
		stdin, _ = io.ReadAll(os.Stdin)
	}
	record, _ := os.CreateTemp(filepath.Dir(os.Getenv("COLLECTOR_RECORDS")), fmt.Sprintf("%020d-call-", time.Now().UnixNano()))
	_ = json.NewEncoder(record).Encode(collectorCall{args, string(stdin)})
	_ = record.Close()
	_ = os.Rename(record.Name(), filepath.Join(os.Getenv("COLLECTOR_RECORDS"), filepath.Base(record.Name())))
	exit := 0
	switch name {
	case "timeout":
		_ = syscall.Exec(os.Getenv("COLLECTOR_TIMEOUT"), append([]string{"timeout"}, a...), os.Environ())
		exit = 1
	case "id":
		if a[0] == "-u" && os.Getenv("COLLECTOR_ROOT") == "1" {
			fmt.Print("0\n")
		} else {
			fmt.Print("1000\n")
		}
	case "date":
		if slices.Contains(a, "+%s") {
			if barrier := os.Getenv("COLLECTOR_BARRIER"); barrier != "" {
				_ = os.WriteFile(barrier+".ready", nil, 0600)
				for {
					if _, err := os.Stat(barrier + ".resume"); err == nil {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			fmt.Print("1795370400\n")
		} else {
			fmt.Print("2026-11-22T18:00:00Z\n")
		}
	case "flock":
		fd, _ := strconv.Atoi(a[len(a)-1])
		if slices.Contains(a, "-u") {
			if syscall.Flock(fd, syscall.LOCK_UN) != nil {
				exit = 1
			}
			break
		}
		deadline := time.Now()
		if i := slices.Index(a, "-w"); i >= 0 {
			seconds, _ := strconv.Atoi(a[i+1])
			deadline = deadline.Add(time.Duration(seconds) * time.Second)
		} else if !slices.Contains(a, "-n") {
			deadline = deadline.Add(time.Minute)
		}
		for syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) != nil {
			if time.Now().After(deadline) {
				exit = 1
				if i := slices.Index(a, "-E"); i >= 0 {
					exit, _ = strconv.Atoi(a[i+1])
				}
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	case "stat":
		info, err := os.Stat(a[len(a)-1])
		if err != nil {
			exit = 1
		} else {
			switch a[1] {
			case "%Y":
				fmt.Println(info.ModTime().Unix())
			case "%s":
				fmt.Println(info.Size())
			default:
				exit = 1
			}
		}
	case "df":
		fmt.Print("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/root 100 40 50 40% /\n")
	case "mv":
		if os.Getenv("COLLECTOR_RENAME_FAIL") == "1" && strings.HasSuffix(a[len(a)-1], "/facts.json") {
			exit = 1
		} else if err := os.Rename(a[len(a)-2], a[len(a)-1]); err != nil {
			exit = 1
		}
	case "docker":
		switch a[0] {
		case "ps":
			if slices.Contains(a, "label=com.docker.compose.service=postgres") {
				switch os.Getenv("COLLECTOR_POSTGRES") {
				case "missing":
				case "multiple":
					fmt.Println(strings.Repeat("b", 64))
					fmt.Println(strings.Repeat("c", 64))
				case "invalid":
					fmt.Println("--bad-id")
				default:
					fmt.Println(strings.Repeat("b", 64))
				}
			} else {
				if os.Getenv("COLLECTOR_BAD_ID") == "1" {
					fmt.Println("--bad-id")
				} else {
					fmt.Println(strings.Repeat("a", 64))
				}
			}
		case "inspect":
			if os.Getenv("COLLECTOR_SLOW") == "1" {
				time.Sleep(10 * time.Second)
			}
			fmt.Print("{\"project\":\"sample\",\"service\":\"api\",\"name\":\"/sample-api-1\",\"state\":\"running\",\"health\":null}\n")
			if os.Getenv("COLLECTOR_OVERFLOW") == "1" {
				fmt.Print(strings.Repeat("x", 2*1024*1024))
			}
			if os.Getenv("COLLECTOR_INSPECT_FAIL") == "1" {
				fmt.Fprintln(os.Stderr, "PRIVATE_DIAGNOSTIC")
				exit = 1
			}
		case "exec":
			if slices.Contains(a, "psql") {
				if strings.Contains(string(stdin), "river_job") {
					if os.Getenv("COLLECTOR_SQL_FAIL") != "" {
						fmt.Fprintln(os.Stderr, "ERROR:", os.Getenv("COLLECTOR_SQL_FAIL"))
						exit = 3
					}
					fmt.Print("[]\n")
				} else {
					fmt.Print("{\"archive_mode\":\"on\",\"archived_at\":null,\"failed_at\":null}\n")
				}
			} else {
				fmt.Print("[]\n")
			}
		case "rm":
			// Docker removes only the named temporary projector.
		case "run":
			if os.Getenv("COLLECTOR_PROJECT_SLOW") == "1" {
				time.Sleep(10 * time.Second)
			}
			var mount string
			for i, arg := range a {
				if arg == "--mount" {
					mount = a[i+1]
				}
			}
			src := strings.TrimSuffix(strings.TrimPrefix(mount, "type=bind,src="), ",dst=/input,readonly")
			err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				rel, _ := filepath.Rel(src, path)
				dest := filepath.Join(os.Getenv("COLLECTOR_CAPTURED"), rel)
				if d.IsDir() {
					return os.MkdirAll(dest, 0700)
				}
				data, e := os.ReadFile(path)
				if e != nil {
					return e
				}
				return os.WriteFile(dest, data, 0600)
			})
			if err != nil {
				exit = 1
			}
			info, err := os.Stat(src)
			if err == nil {
				_ = os.WriteFile(filepath.Join(os.Getenv("COLLECTOR_CAPTURED"), "stage-mode"), []byte(fmt.Sprintf("%o", info.Mode().Perm())), 0600)
			}
			fmt.Print("{\"version\":1,\"app\":\"sample\",\"env\":\"production\",\"profile\":\"lite\",\"observed_at\":\"2026-11-22T18:00:00Z\"}\n")
			if os.Getenv("COLLECTOR_PROJECT_OVERFLOW") == "1" {
				fmt.Print(strings.Repeat("x", 393217))
			}
			if os.Getenv("COLLECTOR_PROJECT_FAIL") == "1" {
				exit = 1
			}
		default:
			exit = 1
		}
	default:
		exit = 1
	}
	os.Exit(exit)
}

func TestCollectorLimits(t *testing.T) {
	for _, tc := range []struct{ name, env, input, reason string }{
		{"overflow", "COLLECTOR_OVERFLOW=1", "containers.ndjson", "invalid"},
		{"timeout", "COLLECTOR_SLOW=1", "containers.ndjson", "timeout"},
		{"failed partial", "COLLECTOR_INSPECT_FAIL=1", "containers.ndjson", "source_down"},
		{"unsafe ID", "COLLECTOR_BAD_ID=1", "containers.ndjson", "invalid"},
		{"missing postgres", "COLLECTOR_POSTGRES=missing", "jobs.json", "missing"},
		{"ambiguous postgres", "COLLECTOR_POSTGRES=multiple", "jobs.json", "invalid"},
		{"unsafe postgres", "COLLECTOR_POSTGRES=invalid", "jobs.json", "invalid"},
		{"missing jobs table", "COLLECTOR_SQL_FAIL=42P01", "jobs.json", "missing"},
		{"other SQL error", "COLLECTOR_SQL_FAIL=42501", "jobs.json", "source_down"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCollector(t)
			f.env = append(f.env, tc.env)
			out, err := f.run()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if strings.Contains(string(out), "PRIVATE_DIAGNOSTIC") {
				t.Fatal("command diagnostic escaped")
			}
			if got := f.input(tc.input + ".err"); got != tc.reason+"\n" {
				t.Fatalf("reason = %q, want %s", got, tc.reason)
			}
			if _, err := os.Stat(filepath.Join(f.captured, tc.input)); !os.IsNotExist(err) {
				t.Fatal("failed partial input survived")
			}
			for _, c := range f.calls() {
				if tc.name == "unsafe ID" && len(c.Args) > 1 && c.Args[0] == "docker" && c.Args[1] == "inspect" {
					t.Fatal("inspected an unsafe ID")
				}
				if strings.Contains(tc.name, "postgres") && len(c.Args) > 1 && c.Args[0] == "docker" && c.Args[1] == "exec" {
					t.Fatal("executed a probe without one valid postgres container")
				}
			}
		})
	}
	t.Run("large file and complete suffix", func(t *testing.T) {
		f := newCollector(t)
		f.write("current/release.json", strings.Repeat("x", 2097153), 0600)
		f.write("deploy.state", strings.Repeat("x", 1048576)+"\ncomplete\npartial", 0600)
		requireCollector(t, f)
		if got := f.input("release.json.err"); got != "invalid\n" {
			t.Fatal(got)
		}
		if got := f.input("deploy.state"); got != "complete\n" {
			t.Fatalf("suffix = %q", got)
		}
	})
	t.Run("configuration", func(t *testing.T) {
		for _, bad := range []string{
			"sample\nproduction\nlite\nnone\nfalse\nimage:tag\nhttps://sample.example\nextra\n",
			"sample\nproduction\nlite\nnone\nfalse\n--privileged\nhttps://sample.example\n",
			"sample\nproduction\nlite\nnone\nfalse\n$(touch pwned)\nhttps://sample.example\n",
			"sample\nproduction\nlite\nnone\nfalse\nimage:tag\nhttps://sample.example",
			"sample\nproduction\nlite\nnone\nfalse\nimage:tag\nhttps://user:secret@sample.example\n",
			"../sample\nproduction\nlite\nnone\nfalse\nimage:tag\nhttps://sample.example\n",
			"sample\x00\nproduction\nlite\nnone\nfalse\nimage:tag\nhttps://sample.example\n",
			"sample\nproduction\nunknown\nnone\nfalse\nimage:tag\nhttps://sample.example\n",
		} {
			f := newCollector(t)
			f.write("console/probe.conf", bad, 0600)
			if out, err := f.run(); err == nil {
				t.Fatalf("accepted configuration %q: %s", bad, out)
			}
			for _, c := range f.calls() {
				if c.Args[0] == "docker" {
					t.Fatal("docker ran for invalid configuration")
				}
			}
		}
	})
}

func TestCollectorAtomic(t *testing.T) {
	for _, failure := range []string{"COLLECTOR_PROJECT_FAIL=1", "COLLECTOR_PROJECT_OVERFLOW=1", "COLLECTOR_PROJECT_SLOW=1", "COLLECTOR_RENAME_FAIL=1"} {
		t.Run(failure, func(t *testing.T) {
			f := newCollector(t)
			if err := os.Mkdir(filepath.Join(f.dir, "data"), 0755); err != nil {
				t.Fatal(err)
			}
			f.write("console/data/facts.json", "old sample\n", 0644)
			f.env = append(f.env, failure)
			if out, err := f.run(); err == nil {
				t.Fatalf("failure was ignored: %s", out)
			}
			data, err := os.ReadFile(filepath.Join(f.dir, "data/facts.json"))
			if err != nil || string(data) != "old sample\n" {
				t.Fatalf("previous facts changed: %s, %v", data, err)
			}
			removed := false
			for _, c := range f.calls() {
				if len(c.Args) > 1 && c.Args[0] == "docker" && c.Args[1] == "rm" {
					removed = true
				}
			}
			if !removed {
				t.Fatal("temporary projector was not removed")
			}
			assertCollectorClean(t, f)
		})
	}
	t.Run("existing lock", func(t *testing.T) {
		f := newCollector(t)
		lock, err := os.OpenFile(filepath.Join(f.dir, "sample.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
		requireCollector(t, f)
		for _, c := range f.calls() {
			if c.Args[0] == "docker" {
				t.Fatal("concurrent sampler ran docker")
			}
		}
	})
	t.Run("if old", func(t *testing.T) {
		f := newCollector(t)
		requireCollector(t, f)
		directoryBefore, err := os.Stat(filepath.Join(f.dir, "data"))
		if err != nil {
			t.Fatal(err)
		}
		before := len(f.calls())
		if out, err := f.run("--if-old"); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		for _, c := range f.calls()[before:] {
			if c.Args[0] == "docker" {
				t.Fatal("fresh sample was replaced")
			}
		}
		// A recent file mtime must not conceal an old observation.
		f.write("console/data/facts.json", `{"version":1,"app":"sample","env":"production","profile":"lite","observed_at":"2026-11-22T17:59:29Z"}`, 0644)
		before = len(f.calls())
		if out, err := f.run("--if-old"); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		found := false
		for _, c := range f.calls()[before:] {
			if c.Args[0] == "docker" {
				found = true
			}
		}
		if !found {
			t.Fatal("expired sample was reused")
		}
		directoryAfter, err := os.Stat(filepath.Join(f.dir, "data"))
		if err != nil || !os.SameFile(directoryBefore, directoryAfter) {
			t.Fatalf("data directory inode changed: %v", err)
		}
	})
	t.Run("signal", func(t *testing.T) {
		f := newCollector(t)
		f.env = append(f.env, "COLLECTOR_SLOW=1")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/bin/sh", filepath.Join(f.dir, "collect.sh"))
		cmd.Env = f.env
		// The sampler gets its own process group. The slow fake docker outlives the
		// sampler, and it must stop before the test removes the directory.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		group := cmd.Process.Pid
		t.Cleanup(func() {
			_ = syscall.Kill(-group, syscall.SIGKILL)
			for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
				if err := syscall.Kill(-group, 0); err != nil {
					return
				}
			}
		})
		deadline := time.Now().Add(10 * time.Second)
		ready := false
		for time.Now().Before(deadline) {
			for _, c := range f.calls() {
				if len(c.Args) > 1 && c.Args[0] == "docker" && c.Args[1] == "inspect" {
					ready = true
				}
			}
			if ready {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !ready {
			t.Fatal("probe did not start")
		}
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("signal was ignored")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("signal did not stop the sampler")
		}
		assertCollectorClean(t, f)
	})
}
func assertCollectorClean(t *testing.T, f *collectorFixture) {
	t.Helper()
	for _, pattern := range []string{".probe.*", "data/.facts.*"} {
		paths, err := filepath.Glob(filepath.Join(f.dir, pattern))
		if err != nil || len(paths) != 0 {
			t.Fatalf("staging survived: %v, %v", paths, err)
		}
	}
}

func TestCollectorDisabled(t *testing.T) {
	for _, tc := range []struct {
		backend, jobs string
		execs         int
	}{{"none", "false", 0}, {"pgdump", "false", 0}, {"none", "true", 1}, {"walg", "false", 2}} {
		t.Run(tc.backend+"-"+tc.jobs, func(t *testing.T) {
			f := newCollector(t)
			f.configure(tc.backend, tc.jobs)
			requireCollector(t, f)
			execs := 0
			for _, c := range f.calls() {
				if len(c.Args) > 1 && c.Args[0] == "docker" && c.Args[1] == "exec" {
					execs++
				}
			}
			if execs != tc.execs {
				t.Fatalf("execs=%d, want %d", execs, tc.execs)
			}
			for _, name := range []string{"jobs.json", "wal.json", "backup.json"} {
				disabled := name == "jobs.json" && tc.jobs == "false" || name == "wal.json" && (tc.backend == "none" || tc.backend == "pgdump") || name == "backup.json" && tc.backend == "none"
				if disabled {
					for _, suffix := range []string{"", ".err"} {
						if _, err := os.Stat(filepath.Join(f.captured, name+suffix)); !os.IsNotExist(err) {
							t.Fatalf("disabled input exists: %s%s", name, suffix)
						}
					}
				}
			}
		})
	}
}

func TestCollectorPermissions(t *testing.T) {
	f := newCollector(t)
	requireCollector(t, f)
	for path, want := range map[string]os.FileMode{"console/data": 0755, "console/data/facts.json": 0644, "console/sample.lock": 0600, "console/config.lock": 0600, "console/probe.conf": 0600, "console/collect.sh": 0700} {
		info, err := os.Stat(filepath.Join(f.root, path))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode %o, want %o", path, info.Mode().Perm(), want)
		}
	}
	if got := f.input("stage-mode"); got != "700" {
		t.Fatalf("private stage mode %s", got)
	}
	assertCollectorClean(t, f)
	t.Run("root rejected", func(t *testing.T) {
		f := newCollector(t)
		f.env = append(f.env, "COLLECTOR_ROOT=1")
		if out, err := f.run(); err == nil {
			t.Fatalf("root accepted: %s", out)
		}
		for _, c := range f.calls() {
			if c.Args[0] == "docker" {
				t.Fatal("root ran docker")
			}
		}
	})
}

func TestCollectorCommandsSnapshots(t *testing.T) {
	f := newCollector(t)
	for i := 0; i < 25; i++ {
		id := fmt.Sprintf("20261122T1800%02dZ-0123456789ab", i)
		dir := filepath.Join(f.root, "releases", id)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		f.write("releases/"+id+"/release.json", `{"release":"`+id+`"}`, 0600)
	}
	outside := filepath.Join(f.root, "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	f.write("outside/release.json", "SECRET", 0600)
	if err := os.Symlink(outside, filepath.Join(f.root, "releases", "20261122T180059Z-0123456789ab")); err != nil {
		t.Fatal(err)
	}
	// A regular release directory must not allow a symlink to escape either.
	linked := filepath.Join(f.root, "releases", "20261122T180024Z-0123456789ab", "release.json")
	if err := os.Remove(linked); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "release.json"), linked); err != nil {
		t.Fatal(err)
	}
	requireCollector(t, f)
	entries, err := os.ReadDir(filepath.Join(f.captured, "snapshots"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 19 {
		t.Fatalf("snapshots=%d, want 19 safe files among the newest 20 directories", len(entries))
	}
	for _, e := range entries {
		if strings.Contains(f.input("snapshots/"+e.Name()), "SECRET") {
			t.Fatal("followed a snapshot symlink")
		}
	}
	if _, err := os.Stat(filepath.Join(f.captured, "snapshots/20261122T180004Z-0123456789ab.json")); !os.IsNotExist(err) {
		t.Fatal("included a snapshot outside the newest 20")
	}
}

func TestCollectorCommandsPGDump(t *testing.T) {
	f := newCollector(t)
	f.configure("pgdump", "false")
	if err := os.MkdirAll(filepath.Join(f.root, "backups/pgdump"), 0700); err != nil {
		t.Fatal(err)
	}
	f.write("backups/pgdump/20261122T170000ZF.dump", "dump", 0600)
	f.write("backups/pgdump/20261122T175000ZF.dump.tmp", "incomplete", 0600)
	f.write("backups/pgdump/arbitrary.dump", "invalid label", 0600)
	if err := os.Symlink(filepath.Join(f.root, "current/release.json"), filepath.Join(f.root, "backups/pgdump/20261122T180000ZF.dump")); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 11, 22, 17, 1, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(f.root, "backups/pgdump/20261122T170000ZF.dump"), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	requireCollector(t, f)
	var rows []struct {
		Label      string `json:"label"`
		FinishedAt string `json:"finished_at"`
		Size       int    `json:"size_bytes"`
	}
	if err := json.Unmarshal([]byte(f.input("backup.json")), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Label != "20261122T170000ZF" || rows[0].FinishedAt != "2026-11-22T17:01:00Z" || rows[0].Size != 4 {
		t.Fatalf("completed dumps: %+v", rows)
	}
}

func TestCollectorLimitsAlignedJournal(t *testing.T) {
	f := newCollector(t)
	// The byte preceding this suffix is LF, so its first complete entry must survive.
	suffix := "complete\n" + strings.Repeat("x", 1048576-len("complete\n")-1) + "\n"
	f.write("deploy.state", "discarded\n"+suffix, 0600)
	requireCollector(t, f)
	if got := f.input("deploy.state"); got != suffix {
		t.Fatalf("aligned journal suffix lost a complete entry: got %d bytes, want %d", len(got), len(suffix))
	}
}
