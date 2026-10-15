package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stopyellingatme/spinup/internal/remote"
)

func TestJournalAppendRead(t *testing.T) {
	t.Parallel()
	f := &remote.Fake{}
	ctx := context.Background()
	j := Journal{Remote: f, App: "app", Release: "r1", Now: func() time.Time { return fixedTime }}
	for range 3 {
		f.Expect("sh", "-c").Return("", "", 0)
	}
	if err := j.Start(ctx, "build"); err != nil {
		t.Fatal(err)
	}
	detail := "ok 'quote'\n$(no)"
	if err := j.Done(ctx, "build", detail); err != nil {
		t.Fatal(err)
	}
	if err := j.Fail(ctx, "health", errors.New("unhealthy")); err != nil {
		t.Fatal(err)
	}
	statuses := []string{"started", "done", "failed"}
	for i, c := range f.Calls() {
		if !strings.Contains(c.Argv[2], " >> '/opt/app/deploy.state'") || !strings.Contains(c.Argv[2], `"status":"`+statuses[i]+`"`) {
			t.Fatalf("append = %#v", c)
		}
	}
	entry := Entry{Release: "r1", Step: "build", Status: "done", At: fixedTime.UTC(), Detail: detail}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	f.Expect("sh", "-c").Return(string(data)+"\n", "", 0)
	got, err := ReadJournal(ctx, f, "app")
	if err != nil || !reflect.DeepEqual(got, []Entry{entry}) {
		t.Fatalf("read = %#v, %v", got, err)
	}
	f.AssertDone(t)
}

func TestNextStep(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		entries []Entry
		want    string
		ok      bool
	}{
		{name: "empty", want: "build", ok: true},
		{name: "other release", entries: []Entry{{Release: "other", Step: "build", Status: "done"}}, want: "build", ok: true},
		{name: "resume failed", entries: []Entry{{Release: "r", Step: "build", Status: "done"}, {Release: "r", Step: "health", Status: "failed"}}, want: "health", ok: true},
		{name: "restarted", entries: []Entry{{Release: "r", Step: "build", Status: "done"}, {Release: "r", Step: "build", Status: "started"}}, want: "build", ok: true},
		{name: "complete", entries: []Entry{{Release: "r", Step: "health", Status: "done"}, {Release: "r", Step: "build", Status: "done"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, ok := NextStep(tt.entries, []string{"build", "health"}, "r")
			if s != tt.want || ok != tt.ok {
				t.Fatalf("next = %q, %v", s, ok)
			}
		})
	}
}

func TestJournalShellRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	vm := localVM{root: t.TempDir()}
	j := Journal{Remote: vm, App: "app", Release: "r1", Now: func() time.Time { return fixedTime }}
	entries, err := ReadJournal(ctx, vm, "app")
	if err != nil || len(entries) != 0 {
		t.Fatalf("absent journal = %v, %v", entries, err)
	}
	detail := "quotes ' \" and newlines\n$(false) `false`"
	if err := j.Start(ctx, "build"); err != nil {
		t.Fatal(err)
	}
	if err := j.Done(ctx, "build", detail); err != nil {
		t.Fatal(err)
	}
	if err := j.Fail(ctx, "health", errors.New("broken")); err != nil {
		t.Fatal(err)
	}
	entries, err = ReadJournal(ctx, vm, "app")
	if err != nil || len(entries) != 3 || entries[1].Detail != detail {
		t.Fatalf("roundtrip = %#v, %v", entries, err)
	}
	step, ok := NextStep(entries, []string{"build", "health"}, "r1")
	if !ok || step != "health" {
		t.Fatalf("resume = %q, %v", step, ok)
	}
}

func TestReadJournalInvalidEntries(t *testing.T) {
	t.Parallel()
	for _, data := range []string{`{`, "{}\n", `{"release":"r","step":"build","status":"unknown","at":"2026-10-15T07:00:00Z"}`} {
		t.Run(data, func(t *testing.T) {
			t.Parallel()
			f := &remote.Fake{}
			f.Expect("sh", "-c").Return(data, "", 0)
			if _, err := ReadJournal(context.Background(), f, "app"); err == nil {
				t.Fatal("invalid journal accepted")
			}
			f.AssertDone(t)
		})
	}
}
