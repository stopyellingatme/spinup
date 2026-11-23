package console

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReadConfig(t *testing.T) {
	t.Parallel()
	identity := map[string]string{"CONSOLE_APP": "todo", "CONSOLE_ENV": "production", "CONSOLE_PROFILE": "lite"}
	for _, tc := range []struct {
		name string
		vars map[string]string
		want config
	}{
		{"query identity", identity, config{App: "todo", Env: "production", Profile: "lite"}},
		{"page", map[string]string{
			"CONSOLE_APP": "todo", "CONSOLE_ENV": "production", "CONSOLE_PROFILE": "full",
			"CONSOLE_ORIGIN": "https://todo.example:8443", "CONSOLE_BACKEND": "pgbackrest", "CONSOLE_JOBS": "true",
		}, config{App: "todo", Env: "production", Profile: "full", Origin: "https://todo.example:8443", Backend: "pgbackrest", Jobs: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readConfig(func(key string) string { return tc.vars[key] })
			if err != nil || got != tc.want {
				t.Fatalf("readConfig = %+v, %v, want %+v", got, err, tc.want)
			}
		})
	}
	for _, tc := range []struct{ key, value string }{
		{"CONSOLE_APP", ""}, {"CONSOLE_APP", "../todo"}, {"CONSOLE_APP", strings.Repeat("a", 64)},
		{"CONSOLE_ENV", ""}, {"CONSOLE_ENV", "prod/other"}, {"CONSOLE_ENV", "-prod"},
		{"CONSOLE_PROFILE", ""}, {"CONSOLE_PROFILE", "unknown"},
		{"CONSOLE_BACKEND", "unknown"}, {"CONSOLE_JOBS", "1"}, {"CONSOLE_JOBS", "FALSE"},
		{"CONSOLE_ORIGIN", "/relative"}, {"CONSOLE_ORIGIN", "ftp://todo.example"},
		{"CONSOLE_ORIGIN", "https://user:secret@todo.example"}, {"CONSOLE_ORIGIN", "https://todo.example/path"},
		{"CONSOLE_ORIGIN", "https://todo.example?x=1"}, {"CONSOLE_ORIGIN", "https://todo.example?"},
		{"CONSOLE_ORIGIN", "https://todo.example#fragment"}, {"CONSOLE_ORIGIN", "https://todo.example#"},
		{"CONSOLE_ORIGIN", "https://"}, {"CONSOLE_ORIGIN", "https://todo.example\n"},
		{"CONSOLE_APP", "todo\x00"}, {"CONSOLE_ENV", "prod\r"},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			vars := map[string]string{}
			for key, value := range identity {
				vars[key] = value
			}
			vars[tc.key] = tc.value
			_, err := readConfig(func(key string) string { return vars[key] })
			if err == nil || !strings.Contains(err.Error(), tc.key) || !strings.Contains(err.Error(), "\nTry:") {
				t.Fatalf("invalid %s accepted or lacks repair: %v", tc.key, err)
			}
		})
	}
	for key, values := range map[string][]string{
		"CONSOLE_PROFILE": {"absent", "none", "lite", "full"},
		"CONSOLE_BACKEND": {"none", "pgbackrest", "pgdump", "walg"},
		"CONSOLE_JOBS":    {"true", "false"},
	} {
		for _, value := range values {
			vars := map[string]string{}
			for name, v := range identity {
				vars[name] = v
			}
			vars[key] = value
			if _, err := readConfig(func(name string) string { return vars[name] }); err != nil {
				t.Fatalf("valid %s=%s: %v", key, value, err)
			}
		}
	}
}

func TestWireSections(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)
	empty := []queue{}
	data, err := json.Marshal(section[[]queue]{State: "ok", At: &at, Data: &empty})
	if err != nil || string(data) != `{"state":"ok","reason":null,"at":"2026-11-22T18:00:00Z","data":[]}` {
		t.Fatalf("empty queue = %s, %v", data, err)
	}
	reason := "no_jobs"
	data, err = json.Marshal(section[[]queue]{State: "disabled", Reason: &reason})
	if err != nil || string(data) != `{"state":"disabled","reason":"no_jobs","at":null,"data":null}` {
		t.Fatalf("disabled queue = %s, %v", data, err)
	}
	data, err = json.Marshal(section[release]{State: "ok", At: &at})
	if err != nil || string(data) != `{"state":"ok","reason":null,"at":"2026-11-22T18:00:00Z","data":null}` {
		t.Fatalf("absent release = %s, %v", data, err)
	}
}

func TestWireRecordKeys(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		value any
		keys  string
	}{
		{overview{}, "version,app,env,profile,observed_at,release,containers,alerts,backup,wal,jobs,disk,memory,history"},
		{release{}, "id,git_sha,color,created_at,age_seconds,previous_id"},
		{container{}, "project,service,name,state,health"},
		{alert{}, "name,severity,since"},
		{backup{}, "backend,label,kind,finished_at,size_bytes"},
		{wal{}, "state,archived_at,failed_at,min_segment,max_segment"},
		{queue{}, "queue,state,count"},
		{space{}, "total_bytes,used_bytes,available_bytes"},
		{attempt{}, "id,at,user,git_sha,color,result"},
		{logs{}, "version,start,end,query,entries,more,explore_path"},
		{line{}, "at,service,level,line"},
	} {
		data, err := json.Marshal(tc.value)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			t.Fatal(err)
		}
		keys := strings.Split(tc.keys, ",")
		if len(object) != len(keys) {
			t.Fatalf("%T has wrong keys: %s", tc.value, data)
		}
		for _, key := range keys {
			if _, ok := object[key]; !ok {
				t.Errorf("%T lacks %s: %s", tc.value, key, data)
			}
		}
		// Field order is also the schema order used by the terminal renderer.
		typ := reflect.TypeOf(tc.value)
		for i, key := range keys {
			if typ.Field(i).Tag.Get("json") != key {
				t.Errorf("%T field %d must use JSON key %s", tc.value, i, key)
			}
		}
	}
}

func TestWireErrorCatalog(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		code         string
		status       int
		message, try string
	}{
		{"invalid_filter", http.StatusBadRequest, "Invalid console query.", "Use the filters shown on the Logs page."},
		{"forbidden", http.StatusForbidden, "Console origin refused.", "Open the URL printed by spinup console open."},
		{"not_found", http.StatusNotFound, "Console route not found.", "Open the console overview."},
		{"method_not_allowed", http.StatusMethodNotAllowed, "This console route accepts GET only.", "Open the console overview."},
		{"monitoring_disabled", http.StatusConflict, "Log search requires monitoring.", "Enable the lite or full monitoring profile and deploy."},
		{"query_too_long", http.StatusRequestURITooLong, "Console query exceeds 8 KiB.", "Shorten the search text."},
		{"busy", http.StatusTooManyRequests, "The console query limit is busy.", "Retry the query."},
		{"source_down", http.StatusServiceUnavailable, "The log source did not answer with valid data.", "Check spinup monitor status."},
	} {
		entry, ok := errorCatalog[tc.code]
		if !ok || entry.Status != tc.status || entry.Message != tc.message || entry.Try != tc.try {
			t.Errorf("%s catalog entry = %+v", tc.code, entry)
		}
	}
	data, err := json.Marshal(wireError{Error: errorDetail{Code: "busy", Message: "The console query limit is busy.", Try: "Retry the query."}})
	if err != nil || string(data) != `{"error":{"code":"busy","message":"The console query limit is busy.","try":"Retry the query."}}` {
		t.Fatalf("error envelope = %s, %v", data, err)
	}
}
