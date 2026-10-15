package deploy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/stopyellingatme/spinup/internal/remote"
)

// Entry records the state of one deployment step in deploy.state.
type Entry struct {
	Release string    `json:"release"`
	Step    string    `json:"step"`
	Status  string    `json:"status"`
	At      time.Time `json:"at"`
	Detail  string    `json:"detail"`
}

// Journal appends step events for one release. Now defaults to time.Now.
// The caller must hold the deploy lock while writing events.
type Journal struct {
	Remote       remote.Runner
	App, Release string
	Now          func() time.Time
}

// Start records that step started.
func (j *Journal) Start(ctx context.Context, step string) error {
	return j.append(ctx, step, "started", "")
}

// Done records that step completed, with optional detail.
func (j *Journal) Done(ctx context.Context, step, detail string) error {
	return j.append(ctx, step, "done", detail)
}

// Fail records that step failed. A nil error produces an empty detail.
func (j *Journal) Fail(ctx context.Context, step string, err error) error {
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	return j.append(ctx, step, "failed", detail)
}

func (j *Journal) append(ctx context.Context, step, status, detail string) error {
	root, err := appRoot(j.App)
	if err != nil {
		return err
	}
	if j.Release == "" || step == "" {
		return fmt.Errorf("journal release or step is empty\nTry: supply a release and step")
	}
	data, err := json.Marshal(Entry{Release: j.Release, Step: step, Status: status, At: utcNow(j.Now), Detail: detail})
	if err != nil {
		return err
	}
	script := "printf '%s\\n' " + remote.Quote(string(data)) + " >> " + remote.Quote(root+"/deploy.state")
	_, err = j.Remote.Run(ctx, remote.Cmd{Argv: []string{"sh", "-c", script}})
	return err
}

// ReadJournal reads the deploy journal. An absent file returns no entries.
// Invalid or partial lines return an error instead of silently advancing a resume.
func ReadJournal(ctx context.Context, r remote.Runner, app string) ([]Entry, error) {
	root, err := appRoot(app)
	if err != nil {
		return nil, err
	}
	path := remote.Quote(root + "/deploy.state")
	result, err := r.Run(ctx, remote.Cmd{Argv: []string{"sh", "-c", "if [ -e " + path + " ]; then cat " + path + "; fi"}})
	if err != nil {
		return nil, err
	}
	entries := []Entry{}
	scanner := bufio.NewScanner(bytes.NewReader(result.Stdout))
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	for scanner.Scan() {
		var e Entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("decode deploy journal line %d: %w\nTry: inspect deploy.state before resuming", len(entries)+1, err)
		}
		if e.Release == "" || e.Step == "" || e.At.IsZero() || (e.Status != "started" && e.Status != "done" && e.Status != "failed") {
			return nil, fmt.Errorf("invalid deploy journal entry at line %d\nTry: inspect deploy.state before resuming", len(entries)+1)
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read deploy journal: %w\nTry: inspect deploy.state before resuming", err)
	}
	return entries, nil
}

// NextStep returns the first step whose latest event for release is not done.
// The boolean is false when all steps completed or the step list is empty.
func NextStep(entries []Entry, steps []string, release string) (string, bool) {
	statuses := make(map[string]string)
	for _, entry := range entries {
		if entry.Release == release {
			statuses[entry.Step] = entry.Status
		}
	}
	for _, step := range steps {
		if statuses[step] != "done" {
			return step, true
		}
	}
	return "", false
}
