package console

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	probeLimit   = 2 * 1024 * 1024
	hostLimit    = 384 * 1024
	journalLimit = 1024 * 1024
)

func project(input fs.FS, cfg config, at time.Time) (overview, error) {
	if at.IsZero() || at.Year() < 1 || at.Year() > 9999 {
		return overview{}, fmt.Errorf("cannot project console facts: the sample time is invalid\nTry: supply a valid CONSOLE_OBSERVED_AT")
	}
	at = at.UTC()
	if cfg.Backend == "" {
		cfg.Backend = "pgbackrest"
	}
	v := overview{Version: 1, App: cfg.App, Env: cfg.Env, Profile: cfg.Profile, ObservedAt: &at}
	v.Release = projectSection(input, "release.json", at, func(data []byte) (*release, string) {
		return projectRelease(input, cfg, data, at)
	})
	v.Containers = projectSection(input, "containers.ndjson", at, func(data []byte) (*[]container, string) { return projectContainers(data, cfg.App) })
	v.Disk = projectSection(input, "disk.txt", at, projectDisk)
	v.Memory = projectSection(input, "memory.txt", at, projectMemory)
	v.History = projectSection(input, "releases.json", at, func(data []byte) (*[]attempt, string) { return projectHistory(input, cfg, data, at) })
	v.Alerts = failedSection[[]alert]("missing", at)
	// The host sampler does not query Prometheus.
	v.Alerts.At = nil
	switch cfg.Profile {
	case "absent":
		v.Alerts = disabledSection[[]alert]("no_monitoring")
	case "none":
		v.Alerts = disabledSection[[]alert]("profile_none")
	}
	v.Jobs = disabledSection[[]queue]("no_jobs")
	if cfg.Jobs {
		v.Jobs = projectSection(input, "jobs.json", at, projectQueues)
	}
	v.Backup = disabledSection[backup]("no_backups")
	v.WAL = disabledSection[wal]("no_backups")
	if cfg.Backend != "none" {
		v.Backup = projectSection(input, "backup.json", at, func(data []byte) (*backup, string) { return projectBackup(data, cfg, at) })
		v.WAL = disabledSection[wal]("no_wal")
		if cfg.Backend != "pgdump" {
			v.WAL = projectSection(input, "wal.json", at, func(data []byte) (*wal, string) { return projectWAL(input, cfg, data, at) })
		}
	}
	boundHost(&v, at)
	return v, nil
}

func failedSection[T any](reason string, at time.Time) section[T] {
	return section[T]{State: "unavailable", Reason: &reason, At: &at}
}

func disabledSection[T any](reason string) section[T] {
	return section[T]{State: "disabled", Reason: &reason}
}

func projectSection[T any](input fs.FS, name string, at time.Time, parse func([]byte) (*T, string)) section[T] {
	data, reason := readProbe(input, name)
	if reason != "" {
		return failedSection[T](reason, at)
	}
	value, reason := parse(data)
	if reason != "" {
		return failedSection[T](reason, at)
	}
	s := section[T]{State: "ok", At: &at, Data: value}
	encoded, err := json.Marshal(s)
	if err != nil || len(encoded) > hostLimit {
		return failedSection[T]("invalid", at)
	}
	return s
}

// readProbe never exposes probe diagnostics. Error markers contain only a reason.
func readProbe(input fs.FS, name string) ([]byte, string) {
	if data, reason := readInput(input, name+".err"); reason != "missing" {
		if reason != "" {
			return nil, reason
		}
		reason = strings.TrimSpace(string(data))
		if !slices.Contains([]string{"missing", "timeout", "invalid", "source_down"}, reason) {
			reason = "invalid"
		}
		return nil, reason
	}
	return readInput(input, name)
}

func readInput(input fs.FS, name string) ([]byte, string) {
	// Inspect every component before opening. In particular, snapshots cannot be links.
	parent := "."
	parts := strings.Split(name, "/")
	for i, part := range parts {
		entries, err := fs.ReadDir(input, parent)
		if err != nil {
			return nil, inputReason(err)
		}
		found := false
		for _, entry := range entries {
			if entry.Name() == part {
				if entry.Type()&fs.ModeSymlink != 0 || (i == len(parts)-1 && !entry.Type().IsRegular()) || (i < len(parts)-1 && !entry.IsDir()) {
					return nil, "invalid"
				}
				found = true
				break
			}
		}
		if !found {
			return nil, "missing"
		}
		parent = path.Join(parent, part)
	}
	file, err := input.Open(name)
	if err != nil {
		return nil, inputReason(err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, "source_down"
	}
	if !info.Mode().IsRegular() || info.Size() > probeLimit {
		return nil, "invalid"
	}
	data, err := io.ReadAll(io.LimitReader(file, probeLimit+1))
	if err != nil {
		return nil, "source_down"
	}
	if len(data) > probeLimit || !utf8.Valid(data) {
		return nil, "invalid"
	}
	return data, ""
}

func inputReason(err error) string {
	if errors.Is(err, fs.ErrNotExist) {
		return "missing"
	}
	return "source_down"
}

func cleanFact(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

func validTime(at, now time.Time) bool {
	return !at.IsZero() && at.Year() >= 1 && at.Year() <= 9999 && !at.After(now.Add(5*time.Second))
}

func projectContainers(data []byte, app string) (*[]container, string) {
	rows := []container{}
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var c container
		if json.Unmarshal(line, &c) != nil {
			return nil, "invalid"
		}
		if !slices.Contains([]string{app, app + "-blue", app + "-green", app + "-monitoring", app + "-console"}, c.Project) {
			continue
		}
		c.Service, c.Name, c.State = cleanFact(c.Service), strings.TrimPrefix(cleanFact(c.Name), "/"), cleanFact(c.State)
		if c.Health != nil {
			h := cleanFact(*c.Health)
			c.Health = &h
		}
		if c.Service == "" || c.Name == "" || c.State == "" {
			return nil, "invalid"
		}
		rows = append(rows, c)
		if len(rows) > 512 {
			return nil, "invalid"
		}
	}
	slices.SortFunc(rows, func(a, b container) int {
		if n := strings.Compare(a.Project, b.Project); n != 0 {
			return n
		}
		if n := strings.Compare(a.Service, b.Service); n != 0 {
			return n
		}
		return strings.Compare(a.Name, b.Name)
	})
	return &rows, ""
}

func projectQueues(data []byte) (*[]queue, string) {
	var rows []struct {
		Queue, State *string
		Count        *int64
	}
	if json.Unmarshal(data, &rows) != nil || rows == nil || len(rows) > 512 {
		return nil, "invalid"
	}
	out := []queue{}
	seen := map[string]bool{}
	for _, row := range rows {
		if row.Queue == nil || row.State == nil || row.Count == nil || *row.Count < 0 {
			return nil, "invalid"
		}
		q := queue{Queue: cleanFact(*row.Queue), State: cleanFact(*row.State), Count: *row.Count}
		if q.Queue == "" || len(q.Queue) > 128 || !slices.Contains([]string{"available", "cancelled", "completed", "discarded", "pending", "retryable", "running", "scheduled"}, q.State) {
			return nil, "invalid"
		}
		key := q.Queue + "\x00" + q.State
		if seen[key] {
			return nil, "invalid"
		}
		seen[key] = true
		if q.Count != 0 {
			out = append(out, q)
		}
	}
	slices.SortFunc(out, func(a, b queue) int {
		if n := strings.Compare(a.Queue, b.Queue); n != 0 {
			return n
		}
		return strings.Compare(a.State, b.State)
	})
	return &out, ""
}

func kib(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || n > math.MaxInt64/1024 {
		return 0, false
	}
	return n * 1024, true
}

func projectDisk(data []byte) (*space, string) {
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	header := strings.Fields(lines[0])
	if len(lines) != 2 || len(header) < 2 || header[0] != "Filesystem" || header[1] != "1024-blocks" {
		return nil, "invalid"
	}
	cols := strings.Fields(lines[1])
	if len(cols) < 6 {
		return nil, "invalid"
	}
	total, t := kib(cols[1])
	used, u := kib(cols[2])
	available, a := kib(cols[3])
	if !t || !u || !a || used > total || available > total-used {
		return nil, "invalid"
	}
	return &space{total, used, available}, ""
}

func projectMemory(data []byte) (*space, string) {
	values := map[string]int64{}
	for line := range strings.SplitSeq(string(data), "\n") {
		cols := strings.Fields(line)
		if len(cols) == 0 || (cols[0] != "MemTotal:" && cols[0] != "MemAvailable:") {
			continue
		}
		if len(cols) != 3 || cols[2] != "kB" {
			return nil, "invalid"
		}
		if _, exists := values[cols[0]]; exists {
			return nil, "invalid"
		}
		n, ok := kib(cols[1])
		if !ok {
			return nil, "invalid"
		}
		values[cols[0]] = n
	}
	total, t := values["MemTotal:"]
	available, a := values["MemAvailable:"]
	if !t || !a || available > total {
		return nil, "invalid"
	}
	return &space{total, total - available, available}, ""
}

func boundHost(v *overview, at time.Time) {
	type candidate struct {
		size       int
		invalidate func()
	}
	candidates := []candidate{}
	add := func(value any, invalidate func()) {
		b, _ := json.Marshal(value)
		candidates = append(candidates, candidate{len(b), invalidate})
	}
	add(v.Release, func() { v.Release = failedSection[release]("invalid", at) })
	add(v.Containers, func() { v.Containers = failedSection[[]container]("invalid", at) })
	add(v.Backup, func() { v.Backup = failedSection[backup]("invalid", at) })
	add(v.WAL, func() { v.WAL = failedSection[wal]("invalid", at) })
	add(v.Jobs, func() { v.Jobs = failedSection[[]queue]("invalid", at) })
	add(v.Disk, func() { v.Disk = failedSection[space]("invalid", at) })
	add(v.Memory, func() { v.Memory = failedSection[space]("invalid", at) })
	add(v.History, func() { v.History = failedSection[[]attempt]("invalid", at) })
	slices.SortStableFunc(candidates, func(a, b candidate) int { return b.size - a.size })
	for _, c := range candidates {
		b, _ := json.Marshal(v)
		if len(b) <= hostLimit {
			return
		}
		c.invalidate()
	}
}

// These patterns mirror deploy.ValidReleaseID and deploy.LiveColor. The kit
// cannot import the CLI's internal packages.
var (
	factReleaseID = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{12}(-r[1-9][0-9]*)?$`)
	factSHA       = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	factUpstream  = regexp.MustCompile(`(?m)^\s*name \{args\[0\]\}-(blue|green)\s*$`)
	factSegment   = regexp.MustCompile(`^[0-9A-F]{24}$`)
	factWALGLabel = regexp.MustCompile(`^base_[0-9A-F]{24}(_D_[0-9A-F]{24})?$`)
)

type releaseRecord struct {
	ID        string     `json:"release"`
	App       *string    `json:"app"`
	Env       *string    `json:"env"`
	GitSHA    *string    `json:"git_sha"`
	Color     *string    `json:"color"`
	CreatedAt *time.Time `json:"created_at"`
	User      *string    `json:"user"`
}

func validRecord(r *releaseRecord, cfg config, at time.Time) bool {
	if !factReleaseID.MatchString(r.ID) || (r.App != nil && *r.App != cfg.App) || (r.Env != nil && *r.Env != cfg.Env) {
		return false
	}
	if r.GitSHA != nil && (!factSHA.MatchString(*r.GitSHA) || !strings.HasPrefix(*r.GitSHA, r.ID[17:29])) {
		return false
	}
	if r.Color != nil && *r.Color != "blue" && *r.Color != "green" {
		return false
	}
	if r.CreatedAt != nil {
		if !validTime(*r.CreatedAt, at) {
			return false
		}
		utc := r.CreatedAt.UTC()
		r.CreatedAt = &utc
	}
	if r.User != nil {
		user := cleanFact(*r.User)
		if len(user) > 128 {
			return false
		}
		r.User = &user
	}
	return true
}

func releaseRecords(data []byte, cfg config, at time.Time) ([]releaseRecord, string) {
	var rows []releaseRecord
	if json.Unmarshal(data, &rows) != nil || rows == nil {
		return nil, "invalid"
	}
	for i := range rows {
		if !validRecord(&rows[i], cfg, at) {
			return nil, "invalid"
		}
	}
	return rows, ""
}

func projectRelease(input fs.FS, cfg config, data []byte, at time.Time) (*release, string) {
	var r *releaseRecord
	if json.Unmarshal(data, &r) != nil {
		return nil, "invalid"
	}
	if r == nil {
		return nil, ""
	}
	if !validRecord(r, cfg, at) || r.App == nil || r.Env == nil || r.GitSHA == nil || r.Color == nil || r.CreatedAt == nil {
		return nil, "invalid"
	}
	upstream, reason := readProbe(input, "upstream.caddy")
	if reason != "" {
		return nil, reason
	}
	match := factUpstream.FindSubmatch(upstream)
	if match == nil || string(match[1]) != *r.Color {
		return nil, "invalid"
	}
	age := at.Unix() - r.CreatedAt.Unix()
	if at.Nanosecond() < r.CreatedAt.Nanosecond() {
		age--
	}
	age = max(int64(0), age)
	out := &release{ID: r.ID, GitSHA: *r.GitSHA, Color: *r.Color, CreatedAt: *r.CreatedAt, AgeSeconds: age}
	if raw, reason := readProbe(input, "releases.json"); reason == "" {
		if rows, reason := releaseRecords(raw, cfg, at); reason == "" {
			for i, row := range rows {
				if row.ID == r.ID {
					if i+1 < len(rows) && rows[i+1].ID != r.ID {
						id := rows[i+1].ID
						out.PreviousID = &id
					}
					break
				}
			}
		}
	}
	return out, ""
}

type journalRecord struct {
	ID     string    `json:"release"`
	Step   string    `json:"step"`
	Status string    `json:"status"`
	At     time.Time `json:"at"`
}

func projectJournal(data []byte, at time.Time) ([]journalRecord, string) {
	if len(data) > journalLimit {
		data = data[len(data)-journalLimit:]
		first := bytes.IndexByte(data, '\n')
		if first < 0 {
			return []journalRecord{}, ""
		}
		data = data[first+1:]
	}
	last := bytes.LastIndexByte(data, '\n')
	if last < 0 {
		return []journalRecord{}, ""
	}
	rows := []journalRecord{}
	lineIndex := -1
	for line := range bytes.SplitSeq(data[:last], []byte("\n")) {
		lineIndex++
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var row journalRecord
		if json.Unmarshal(line, &row) != nil {
			if lineIndex == 0 {
				// A bounded suffix can start inside a record.
				continue
			}
			return nil, "invalid"
		}
		if !factReleaseID.MatchString(row.ID) {
			continue
		} // Includes the synthetic rollback ID.
		if row.Step == "" || !slices.Contains([]string{"started", "done", "failed"}, row.Status) || !validTime(row.At, at) {
			return nil, "invalid"
		}
		row.At = row.At.UTC()
		rows = append(rows, row)
	}
	return rows, ""
}

func projectHistory(input fs.FS, cfg config, data []byte, at time.Time) (*[]attempt, string) {
	retained, reason := releaseRecords(data, cfg, at)
	if reason != "" {
		return nil, reason
	}
	journal, reason := readProbe(input, "deploy.state")
	if reason != "" {
		return nil, reason
	}
	events, reason := projectJournal(journal, at)
	if reason != "" {
		return nil, reason
	}
	records := map[string]releaseRecord{}
	ids := []string{}
	for _, r := range retained {
		if _, exists := records[r.ID]; !exists {
			records[r.ID] = r
			ids = append(ids, r.ID)
		}
	}
	first := map[string]time.Time{}
	latest := map[string]journalRecord{}
	for _, event := range events {
		if _, exists := records[event.ID]; !exists {
			records[event.ID] = releaseRecord{ID: event.ID}
			ids = append(ids, event.ID)
		}
		if _, exists := first[event.ID]; !exists {
			first[event.ID] = event.At
		}
		latest[event.ID] = event
	}
	// The sampler has already selected the newest directories. Enforce that limit
	// again before reading optional metadata from these fixed snapshot basenames.
	if entries := snapshotEntries(input); entries != nil {
		names := []string{}
		for _, entry := range entries {
			id := strings.TrimSuffix(entry.Name(), ".json")
			if !entry.IsDir() && entry.Type()&fs.ModeSymlink == 0 && strings.HasSuffix(entry.Name(), ".json") && factReleaseID.MatchString(id) {
				names = append(names, entry.Name())
			}
		}
		slices.SortFunc(names, func(a, b string) int { return strings.Compare(b, a) })
		for _, name := range names[:min(20, len(names))] {
			id := strings.TrimSuffix(name, ".json")
			r, exists := records[id]
			if !exists {
				continue
			}
			raw, reason := readProbe(input, "snapshots/"+name)
			if reason != "" {
				continue
			}
			var snapshot releaseRecord
			if json.Unmarshal(raw, &snapshot) != nil || snapshot.ID != id || !validRecord(&snapshot, cfg, at) {
				continue
			}
			if r.User == nil {
				r.User = snapshot.User
			}
			if r.GitSHA == nil {
				r.GitSHA = snapshot.GitSHA
			}
			if r.Color == nil {
				r.Color = snapshot.Color
			}
			if r.CreatedAt == nil {
				r.CreatedAt = snapshot.CreatedAt
			}
			records[id] = r
		}
	}
	rows := []attempt{}
	for _, id := range ids {
		r := records[id]
		when := first[id]
		if r.CreatedAt != nil {
			when = *r.CreatedAt
		}
		if when.IsZero() {
			return nil, "invalid"
		}
		result := "recorded"
		if event, exists := latest[id]; exists {
			result = "incomplete"
			if event.Status == "failed" {
				result = "failed"
			} else if event.Step == "post" && event.Status == "done" {
				result = "succeeded"
			}
		}
		rows = append(rows, attempt{ID: id, At: when, User: r.User, GitSHA: r.GitSHA, Color: r.Color, Result: result})
	}
	slices.SortFunc(rows, func(a, b attempt) int {
		if n := b.At.Compare(a.At); n != 0 {
			return n
		}
		return strings.Compare(a.ID, b.ID)
	})
	rows = rows[:min(20, len(rows))]
	return &rows, ""
}

func snapshotEntries(input fs.FS) []fs.DirEntry {
	entries, err := fs.ReadDir(input, ".")
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if entry.Name() == "snapshots" && entry.IsDir() && entry.Type()&fs.ModeSymlink == 0 {
			snapshots, err := fs.ReadDir(input, "snapshots")
			if err == nil {
				return snapshots
			}
		}
	}
	return nil
}

type pgDatabase struct {
	ID      *int64 `json:"id"`
	RepoKey *int64 `json:"repo-key"`
}

type pgStanza struct {
	Name   string `json:"name"`
	Status struct {
		Code *int `json:"code"`
	} `json:"status"`
	DB   []pgDatabase `json:"db"`
	Repo []struct {
		Key    *int64 `json:"key"`
		Status struct {
			Code *int `json:"code"`
		} `json:"status"`
	} `json:"repo"`
	Archive []struct {
		Database pgDatabase `json:"database"`
		Min, Max *string
	} `json:"archive"`
	Backup []struct {
		Label string `json:"label"`
		Type  string `json:"type"`
		Error bool   `json:"error"`
		Info  struct {
			Size *int64 `json:"size"`
		} `json:"info"`
		Timestamp struct{ Start, Stop *int64 } `json:"timestamp"`
	} `json:"backup"`
}

func readStanza(data []byte, app string) (*pgStanza, string) {
	var rows []pgStanza
	if json.Unmarshal(data, &rows) != nil || rows == nil {
		return nil, "invalid"
	}
	var found *pgStanza
	for i := range rows {
		if rows[i].Name == app {
			if found != nil {
				return nil, "invalid"
			}
			found = &rows[i]
		}
	}
	if found == nil {
		return nil, ""
	}
	if found.Status.Code == nil {
		return nil, "invalid"
	}
	if *found.Status.Code == 1 || *found.Status.Code == 3 {
		return nil, ""
	}
	if *found.Status.Code != 0 {
		return nil, "source_down"
	}
	for _, repo := range found.Repo {
		if repo.Status.Code == nil {
			return nil, "invalid"
		}
		if *repo.Status.Code != 0 {
			return nil, "source_down"
		}
	}
	return found, ""
}

func projectBackup(data []byte, cfg config, at time.Time) (*backup, string) {
	if cfg.Backend == "pgbackrest" {
		stanza, reason := readStanza(data, cfg.App)
		if reason != "" || stanza == nil {
			return nil, reason
		}
		var newest *backup
		for _, b := range stanza.Backup {
			if b.Error {
				continue
			}
			if b.Label == "" || !slices.Contains([]string{"full", "diff", "incr"}, b.Type) || b.Timestamp.Stop == nil || *b.Timestamp.Stop <= 0 || (b.Info.Size != nil && *b.Info.Size < 0) {
				return nil, "invalid"
			}
			finish := time.Unix(*b.Timestamp.Stop, 0).UTC()
			if !validTime(finish, at) || (b.Timestamp.Start != nil && (*b.Timestamp.Start <= 0 || *b.Timestamp.Start > *b.Timestamp.Stop)) {
				return nil, "invalid"
			}
			row := backup{Backend: cfg.Backend, Label: cleanFact(b.Label), Kind: b.Type, FinishedAt: finish, SizeBytes: b.Info.Size}
			if newest == nil || finish.After(newest.FinishedAt) || (finish.Equal(newest.FinishedAt) && row.Label < newest.Label) {
				newest = &row
			}
		}
		return newest, ""
	}
	var rows []struct {
		Label        string     `json:"label"`
		Name         string     `json:"backup_name"`
		FinishedAt   *time.Time `json:"finished_at"`
		Finish       *time.Time `json:"finish_time"`
		Start        *time.Time `json:"start_time"`
		Time         *time.Time `json:"time"`
		Size         *int64     `json:"size_bytes"`
		Uncompressed *int64     `json:"uncompressed_size"`
	}
	if json.Unmarshal(data, &rows) != nil || rows == nil {
		return nil, "invalid"
	}
	var newest *backup
	for _, b := range rows {
		label, kind, finish, size := b.Label, "full", b.FinishedAt, b.Size
		if cfg.Backend == "walg" {
			label, finish, size = b.Name, b.Finish, b.Uncompressed
			if !factWALGLabel.MatchString(label) {
				return nil, "invalid"
			}
			if strings.Contains(label, "_D_") {
				kind = "incr"
			}
			if finish == nil || finish.IsZero() {
				finish = b.Time
			}
			if b.Start != nil && b.Start.IsZero() {
				b.Start = b.Time
			}
		}
		if cfg.Backend == "pgdump" {
			if len(label) != 17 || label[16] != 'F' {
				return nil, "invalid"
			}
			if _, err := time.Parse("20060102T150405Z", label[:16]); err != nil {
				return nil, "invalid"
			}
		}
		if label == "" || finish == nil || !validTime(*finish, at) || (size != nil && *size < 0) || (b.Start != nil && (!validTime(*b.Start, at) || b.Start.After(*finish))) {
			return nil, "invalid"
		}
		row := backup{Backend: cfg.Backend, Label: cleanFact(label), Kind: kind, FinishedAt: finish.UTC(), SizeBytes: size}
		if newest == nil || row.FinishedAt.After(newest.FinishedAt) || (row.FinishedAt.Equal(newest.FinishedAt) && row.Label < newest.Label) {
			newest = &row
		}
	}
	return newest, ""
}

func projectWAL(input fs.FS, cfg config, data []byte, at time.Time) (*wal, string) {
	var observation struct {
		Mode       *string    `json:"archive_mode"`
		ArchivedAt *time.Time `json:"archived_at"`
		FailedAt   *time.Time `json:"failed_at"`
	}
	if json.Unmarshal(data, &observation) != nil || observation.Mode == nil || !slices.Contains([]string{"on", "always", "off"}, *observation.Mode) {
		return nil, "invalid"
	}
	for _, stamp := range []*time.Time{observation.ArchivedAt, observation.FailedAt} {
		if stamp != nil {
			if !validTime(*stamp, at) {
				return nil, "invalid"
			}
			*stamp = stamp.UTC()
		}
	}
	row := &wal{State: "archiving", ArchivedAt: observation.ArchivedAt, FailedAt: observation.FailedAt}
	switch {
	case *observation.Mode == "off":
		row.State = "off"
	case row.FailedAt != nil && (row.ArchivedAt == nil || row.FailedAt.After(*row.ArchivedAt)):
		row.State = "failing"
	case row.ArchivedAt == nil:
		row.State = "waiting"
	}
	// Repository bounds are optional. Repository failure must not erase SQL facts.
	if cfg.Backend == "pgbackrest" {
		if raw, reason := readProbe(input, "backup.json"); reason == "" {
			if stanza, reason := readStanza(raw, cfg.App); reason == "" && stanza != nil {
				row.MinSegment, row.MaxSegment = archiveBounds(stanza)
			}
		}
	}
	return row, ""
}

func archiveBounds(s *pgStanza) (*string, *string) {
	// Select the newest database ID within the repository, never the last archive
	// row. An old database's archive can remain after pg_upgrade or a restore.
	var current *pgDatabase
	for i := range s.DB {
		d := &s.DB[i]
		if d.ID == nil || d.RepoKey == nil || *d.ID <= 0 || *d.RepoKey <= 0 {
			return nil, nil
		}
		if current == nil || *d.ID > *current.ID {
			current = d
		}
	}
	if current == nil {
		return nil, nil
	}
	repositoryOK := false
	for _, r := range s.Repo {
		if r.Key != nil && *r.Key == *current.RepoKey && r.Status.Code != nil && *r.Status.Code == 0 {
			repositoryOK = true
		}
	}
	if !repositoryOK {
		return nil, nil
	}
	selected := -1
	for i, a := range s.Archive {
		if a.Database.ID == nil || a.Database.RepoKey == nil || *a.Database.ID != *current.ID || *a.Database.RepoKey != *current.RepoKey {
			continue
		}
		if selected >= 0 {
			return nil, nil
		}
		selected = i
	}
	if selected < 0 {
		return nil, nil
	}
	low, high := s.Archive[selected].Min, s.Archive[selected].Max
	if low != nil && *low == "" {
		low = nil
	}
	if high != nil && *high == "" {
		high = nil
	}
	if (low != nil && !factSegment.MatchString(*low)) || (high != nil && !factSegment.MatchString(*high)) || (low != nil && high != nil && *low > *high) {
		return nil, nil
	}
	return low, high
}
