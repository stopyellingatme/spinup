package live

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestKey(t *testing.T) {
	for _, s := range []string{"notes", "notes:42", "a_1:9", strings.Repeat("a", 48), "a:123456789012345678"} {
		if err := Key(s).Valid(); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	for _, s := range []string{"", "Notes", "1a", "notes:", "notes:x", strings.Repeat("a", 49), "notes:1234567890123456789", "a:b:2", "a:-1", "a\n", "á"} {
		err := Key(s).Valid()
		if err == nil || !strings.Contains(err.Error(), "^[a-z][a-z0-9_]{0,47}(:[0-9]{1,18})?$") || !strings.Contains(err.Error(), "\nTry:") {
			t.Errorf("%q: %v", s, err)
		}
	}
	if k := OwnedKey("notes", 42); k != "notes:42" || k.Feed() != "notes" || feedOf(k) != "notes" {
		t.Fatal(k)
	}
	if k := SharedKey("notes"); k != "notes" || k.Feed() != "notes" {
		t.Fatal(k)
	}
}

func TestIsStream(t *testing.T) {
	for _, tt := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/live/notes/live", true}, {"GET", "/api/live/notes:42/live", true},
		{"POST", "/api/live/notes/live", false}, {"HEAD", "/api/live/notes/live", false},
		{"GET", "/api/live/notes", false}, {"GET", "/api/lively/notes/live", false},
		{"GET", "/api/live/notes/live/", false}, {"GET", "/api/live", false},
	} {
		r := &http.Request{Method: tt.method, URL: &url.URL{Path: tt.path}}
		if got := IsStream(r); got != tt.want {
			t.Errorf("%s %s: %v", tt.method, tt.path, got)
		}
	}
}

func TestMarshalChanges(t *testing.T) {
	row := struct {
		ID   int    `json:"id"`
		Body string `json:"body"`
	}{7, "x\ny"}
	up := Upsert("7", row)
	for _, tt := range []struct {
		changes []Change
		want    string
	}{
		{[]Change{up}, `[{"op":"upsert","id":"7","row":{"id":7,"body":"x\ny"}}]`},
		{[]Change{Delete("3")}, `[{"op":"delete","id":"3"}]`},
		{[]Change{Touch("4")}, `[{"op":"touch","id":"4"}]`},
		{[]Change{up, Delete("3"), Touch("4")}, `[{"op":"upsert","id":"7","row":{"id":7,"body":"x\ny"}},{"op":"delete","id":"3"},{"op":"touch","id":"4"}]`},
	} {
		got, err := marshalChanges(tt.changes)
		if err != nil || string(got) != tt.want {
			t.Errorf("got %s, %v; want %s", got, err, tt.want)
		}
	}
}

func TestMarshalChangesLimits(t *testing.T) {
	var nilRow *int
	for _, tt := range []struct {
		name    string
		changes []Change
		why     string
	}{
		{"empty", nil, "change"}, {"count", make([]Change, 101), "100"},
		{"empty ID", []Change{Touch("")}, "^[A-Za-z0-9_.:-]{1,64}$"},
		{"invalid ID", []Change{Delete("a/b")}, "^[A-Za-z0-9_.:-]{1,64}$"},
		{"long ID", []Change{Touch(strings.Repeat("a", 65))}, "^[A-Za-z0-9_.:-]{1,64}$"},
		{"op", []Change{{Op: "other", ID: "1"}}, "op"},
		{"nil", []Change{Upsert("1", nil)}, "nil"},
		{"typed nil", []Change{Upsert("1", nilRow)}, "nil"},
		{"delete row", []Change{{Op: OpDelete, ID: "1", Row: 1}}, "row"},
		{"touch row", []Change{{Op: OpTouch, ID: "1", Row: 1}}, "row"},
		{"row bytes", []Change{Upsert("1", strings.Repeat("a", (16<<10)-1))}, "publish live.Touch(id) instead"},
		{"encoded bytes", []Change{Upsert("1", strings.Repeat("<", 3000))}, "publish live.Touch(id) instead"},
		{"patch bytes", []Change{Upsert("1", strings.Repeat("a", (16<<10)-2)), Upsert("2", strings.Repeat("a", (16<<10)-2)), Upsert("3", strings.Repeat("a", (16<<10)-2)), Upsert("4", strings.Repeat("a", (16<<10)-2))}, "65536"},
		{"marshal", []Change{Upsert("1", make(chan int))}, "encode"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := marshalChanges(tt.changes)
			var liveErr *Error
			if !errors.As(err, &liveErr) || !strings.Contains(err.Error(), tt.why) || !strings.Contains(err.Error(), "\nTry: ") {
				t.Fatalf("error: %v", err)
			}
		})
	}
	changes := make([]Change, 100)
	for i := range changes {
		changes[i] = Touch("AZaz09_.:-")
	}
	if _, err := marshalChanges(changes); err != nil {
		t.Fatal(err)
	}
	if _, err := marshalChanges([]Change{Upsert(strings.Repeat("a", 64), strings.Repeat("a", (16<<10)-2))}); err != nil {
		t.Fatal(err)
	}
}

func TestError(t *testing.T) {
	cause := errors.New("database unavailable")
	err := &Error{Op: "publish", Err: cause, Try: "retry later"}
	if err.Error() != "live: publish: database unavailable\nTry: retry later" || !errors.Is(err, cause) {
		t.Fatal(err)
	}
}

func TestMarshalChangesPatchBoundary(t *testing.T) {
	changes := []Change{Upsert("1", ""), Upsert("2", ""), Upsert("3", ""), Upsert("4", "")}
	base, err := marshalChanges(changes)
	if err != nil {
		t.Fatal(err)
	}
	remaining := (64 << 10) - len(base)
	for i := range changes {
		n := min(remaining, (16<<10)-2)
		changes[i].Row = strings.Repeat("a", n)
		remaining -= n
	}
	patch, err := marshalChanges(changes)
	if err != nil || len(patch) != 64<<10 {
		t.Fatalf("boundary: %d %v", len(patch), err)
	}
	changes[3].Row = changes[3].Row.(string) + "a"
	if _, err := marshalChanges(changes); err == nil {
		t.Fatal("accepted patch above 64 KiB")
	}
}
