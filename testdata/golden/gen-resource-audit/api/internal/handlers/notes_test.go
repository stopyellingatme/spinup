package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"example.com/todo/api/db/migrations"
	"example.com/todo/api/internal/gen"
	"example.com/todo/api/internal/handlers"
	"example.com/todo/api/internal/roles"
	"example.com/todo/api/internal/routes"
	"github.com/stopyellingatme/spinup/kit/app"
	"github.com/stopyellingatme/spinup/kit/audit"
	"github.com/stopyellingatme/spinup/kit/audit/authsink"
	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/auth/authtest"
	"github.com/stopyellingatme/spinup/kit/httpx"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

func TestNotesCRUD(t *testing.T) {
	t.Parallel()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL is required for audit events")
	}
	pool := testkit.DBWithMigrations(t, migrations.FS)
	handler := app.New(app.WithRoutes(func(mux *http.ServeMux) {
		routes.Register(mux, routes.Deps{DB: pool})
	})).Handler()
	historyBase := handler
	actorBase := handler
	handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := auth.UserFrom(r.Context())
		actorBase.ServeHTTP(w, r.WithContext(authsink.ActorContext(r.Context(), u)))
	})
	var userA int64
	if err := pool.QueryRow(t.Context(), `INSERT INTO users (email, password_hash) VALUES ('owner-a@example.com', 'test-hash-a') RETURNING id`).Scan(&userA); err != nil {
		t.Fatalf("insert user A: %v", err)
	}
	var userB int64
	if err := pool.QueryRow(t.Context(), `INSERT INTO users (email, password_hash) VALUES ('owner-b@example.com', 'test-hash-b') RETURNING id`).Scan(&userB); err != nil {
		t.Fatalf("insert user B: %v", err)
	}
	other := authtest.AsRole(handler, userB, roles.Manager)
	handler = authtest.AsUser(handler, auth.User{ID: userA, Email: "owner-a@example.com", Role: "manager"})

	rec := testkit.Do(t, handler, http.MethodPost, "/api/notes", strings.NewReader(`{"title": "title 1", "api_token": "api token 1"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d, want %d\n%s", rec.Code, http.StatusCreated, rec.Body)
	}
	created := testkit.DecodeJSON[gen.Note](t, rec)
	path := fmt.Sprintf("/api/notes/%d", created.Id)
	var beforeJSON []byte
	if err := pool.QueryRow(t.Context(), "SELECT to_jsonb(t) FROM notes t WHERE id=$1", created.Id).Scan(&beforeJSON); err != nil {
		t.Fatal(err)
	}
	var before map[string]any
	if err := json.Unmarshal(beforeJSON, &before); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		var body io.Reader
		if method == http.MethodPut {
			body = strings.NewReader(`{"title": "title 2", "api_token": "api token 2"}`)
		}
		rec := testkit.Do(t, other, method, path, body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("other user %s: status %d, want 404: %s", method, rec.Code, rec.Body)
		}
	}
	rec = testkit.Do(t, other, http.MethodGet, "/api/notes", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("other user list: status %d: %s", rec.Code, rec.Body)
	}
	otherList := testkit.DecodeJSON[gen.NoteList](t, rec)
	if otherList.Total != 0 || len(otherList.Items) != 0 {
		t.Fatalf("other user list: got %+v, want empty", otherList)
	}

	rec = testkit.Do(t, handler, http.MethodGet, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: status %d, want %d\n%s", rec.Code, http.StatusOK, rec.Body)
	}
	if got := testkit.DecodeJSON[gen.Note](t, rec); !reflect.DeepEqual(got, created) {
		t.Fatalf("get: got %+v, want %+v", got, created)
	}

	rec = testkit.Do(t, handler, http.MethodGet, "/api/notes?limit=10", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status %d, want %d\n%s", rec.Code, http.StatusOK, rec.Body)
	}
	list := testkit.DecodeJSON[gen.NoteList](t, rec)
	if list.Total != 1 || len(list.Items) != 1 || !reflect.DeepEqual(list.Items[0], created) {
		t.Fatalf("list: got %+v, want one item %+v", list, created)
	}
	// A page past the last row still has the total.
	rec = testkit.Do(t, handler, http.MethodGet, "/api/notes?offset=1", nil)
	if past := testkit.DecodeJSON[gen.NoteList](t, rec); rec.Code != http.StatusOK || past.Total != 1 || len(past.Items) != 0 {
		t.Fatalf("list past the end: status %d, got %+v, want total 1 and no items", rec.Code, past)
	}

	rec = testkit.Do(t, handler, http.MethodPut, path, strings.NewReader(`{"title": "title 2", "api_token": "api token 2"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status %d, want %d\n%s", rec.Code, http.StatusOK, rec.Body)
	}
	updated := testkit.DecodeJSON[gen.Note](t, rec)
	var afterJSON []byte
	if err := pool.QueryRow(t.Context(), "SELECT to_jsonb(t) FROM notes t WHERE id=$1", created.Id).Scan(&afterJSON); err != nil {
		t.Fatal(err)
	}
	var after map[string]any
	if err := json.Unmarshal(afterJSON, &after); err != nil {
		t.Fatal(err)
	}
	if updated.Id != created.Id || reflect.DeepEqual(updated.Title, created.Title) {
		t.Fatalf("update: got %+v, want a new title", updated)
	}
	rec = testkit.Do(t, handler, http.MethodGet, path, nil)
	if got := testkit.DecodeJSON[gen.Note](t, rec); !reflect.DeepEqual(got, updated) {
		t.Fatalf("get after update: got %+v, want %+v", got, updated)
	}

	rec = testkit.Do(t, handler, http.MethodDelete, path, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: status %d, want %d\n%s", rec.Code, http.StatusNoContent, rec.Body)
	}
	rec = testkit.Do(t, handler, http.MethodGet, path, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: status %d, want %d\n%s", rec.Code, http.StatusNotFound, rec.Body)
	}
	history := authtest.AsRole(historyBase, userA, "manager")
	rec = testkit.Do(t, history, http.MethodGet, path+"/history", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("history status %d: %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("history must not be cached")
	}
	events := testkit.DecodeJSON[struct {
		Items []audit.Event `json:"items"`
	}](t, rec).Items
	actions := []string{"delete", "update", "create"}
	if len(events) != len(actions) {
		t.Fatalf("events = %+v, want actions %v", events, actions)
	}
	for i, event := range events {
		if event.Table != "notes" || event.RecordID != strconv.FormatInt(created.Id, 10) {
			t.Fatalf("event subject = %q %q, want %q %d", event.Table, event.RecordID, "notes", created.Id)
		}
		if event.Action != actions[i] || event.Actor.Kind != audit.KindUser || event.Actor.ID != userA || event.Actor.Email != "owner-a@example.com" || event.Actor.Role != "manager" || event.Actor.Source != audit.SourceAPI {
			t.Fatalf("event = %+v", event)
		}
		var changes map[string]map[string]any
		if err := json.Unmarshal(event.Changes, &changes); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"title", "api_token"} {
			oldValue, newValue := before[field], after[field]
			change, present := changes[field]
			if event.Action == "create" && oldValue == nil || event.Action == "update" && reflect.DeepEqual(oldValue, newValue) {
				if present {
					t.Fatalf("unexpected change %s: %v", field, change)
				}
				continue
			}
			if !present {
				t.Fatalf("missing change %s: %s", field, event.Changes)
			}
			redacted := false
			for _, col := range []string{"api_token"} {
				if field == col {
					redacted = true
				}
			}
			if redacted {
				if change["redacted"] != true || len(change) != 1 {
					t.Fatalf("redacted value leaked: %v", change)
				}
				continue
			}
			want := map[string]any{}
			switch event.Action {
			case "create":
				want["new"] = oldValue
			case "update":
				want["old"] = oldValue
				want["new"] = newValue
			case "delete":
				want["old"] = newValue
			}
			if !reflect.DeepEqual(change, want) {
				t.Fatalf("%s %s: got %v, want %v", event.Action, field, change, want)
			}
		}
	}
	otherHistory := authtest.AsRole(historyBase, userB, "manager")
	rec = testkit.Do(t, otherHistory, http.MethodGet, path+"/history", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("other history status %d: %s", rec.Code, rec.Body)
	}
	otherEvents := testkit.DecodeJSON[struct {
		Items []audit.Event `json:"items"`
	}](t, rec)
	if len(otherEvents.Items) != 0 {
		t.Fatalf("another owner saw events: %+v", otherEvents)
	}
}

func TestNotesRejectsBadInput(t *testing.T) {
	t.Parallel()
	// These requests fail before a query runs, so they need no database.
	handler := app.New(app.WithRoutes(func(mux *http.ServeMux) {
		routes.Register(mux, routes.Deps{})
	})).Handler()
	handler = authtest.AsRole(handler, 1, roles.Manager)
	tests := []struct {
		name, method, path, body string
		status                   int
	}{
		{"wrong type", http.MethodPost, "/api/notes", `{"title": 42}`, http.StatusUnprocessableEntity},
		{"not JSON", http.MethodPost, "/api/notes", `{`, http.StatusBadRequest},
		{"body null", http.MethodPost, "/api/notes", `null`, http.StatusUnprocessableEntity},
		{"Post case variant", http.MethodPost, "/api/notes", "{\"TITLE\":null}", http.StatusBadRequest},
		{"Post duplicate name", http.MethodPost, "/api/notes", "{\"title\":null,\"title\":null}", http.StatusBadRequest},
		{"Post unknown member", http.MethodPost, "/api/notes", `{"id":1}`, http.StatusBadRequest},
		{"Post invalid UTF-8", http.MethodPost, "/api/notes", "{\"title\":\"\xff\"}", http.StatusBadRequest},
		{"Put case variant", http.MethodPut, "/api/notes/1", "{\"TITLE\":null}", http.StatusBadRequest},
		{"Put duplicate name", http.MethodPut, "/api/notes/1", "{\"title\":null,\"title\":null}", http.StatusBadRequest},
		{"Put unknown member", http.MethodPut, "/api/notes/1", `{"id":1}`, http.StatusBadRequest},
		{"Put invalid UTF-8", http.MethodPut, "/api/notes/1", "{\"title\":\"\xff\"}", http.StatusBadRequest},
		{"bad id", http.MethodGet, "/api/notes/abc", "", http.StatusBadRequest},
		{"limit too large", http.MethodGet, "/api/notes?limit=201", "", http.StatusUnprocessableEntity},
		{"offset too large", http.MethodGet, "/api/notes?offset=10001", "", http.StatusUnprocessableEntity},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			rec := testkit.Do(t, handler, tc.method, tc.path, body)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d\n%s", rec.Code, tc.status, rec.Body)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
				t.Fatalf("Content-Type %q, want application/problem+json", got)
			}
		})
	}
}
func TestNotesHistoryGuards(t *testing.T) {
	t.Parallel()
	base := app.New(app.WithRoutes(func(mux *http.ServeMux) { routes.Register(mux, routes.Deps{}) })).Handler()
	handler := authtest.AsRole(base, 1, "manager")
	for _, query := range []string{"limit=201", "offset=10001", "limit=0", "offset=-1", "before_id=0", "before_id=-1"} {
		rec := testkit.Do(t, handler, http.MethodGet, "/api/notes/1/history?"+query, nil)
		if rec.Code != http.StatusUnprocessableEntity || rec.Header().Get("Content-Type") != "application/problem+json" || !strings.Contains(rec.Body.String(), "Try:") {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body)
		}
		problem := testkit.DecodeJSON[httpx.Problem](t, rec)
		if problem.Type != "about:blank" || problem.Title != http.StatusText(http.StatusUnprocessableEntity) || problem.Status != http.StatusUnprocessableEntity || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("invalid history problem: %+v", problem)
		}
	}
	denied := authtest.AsRole(base, 1, "__spinup_forbidden_role")
	rec := testkit.Do(t, denied, http.MethodGet, "/api/notes/1/history", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("history status %d, want 403: %s", rec.Code, rec.Body)
	}
	unauthorized := testkit.Do(t, base, http.MethodGet, "/api/notes/1/history", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("history status %d, want 401: %s", unauthorized.Code, unauthorized.Body)
	}
}

func TestNotesRequiresFields(t *testing.T) {
	t.Parallel()
	handler := app.New(app.WithRoutes(func(mux *http.ServeMux) {
		routes.Register(mux, routes.Deps{})
	})).Handler()
	handler = authtest.AsRole(handler, 1, roles.Manager)
	tests := []struct {
		name, body string
		want       []httpx.FieldError
	}{
		{"missing all", "{}", []httpx.FieldError{
			{Field: "api_token", Message: "is required"},
			{Field: "title", Message: "is required"},
		}},
		{"missing api_token", "{\"title\": \"title 1\"}", []httpx.FieldError{
			{Field: "api_token", Message: "is required"},
		}},
		{"null api_token", "{\"title\": \"title 1\", \"api_token\": null}", []httpx.FieldError{
			{Field: "api_token", Message: "is required"},
		}},
		{"missing title", "{\"api_token\": \"api token 1\"}", []httpx.FieldError{
			{Field: "title", Message: "is required"},
		}},
		{"null title", "{\"title\": null, \"api_token\": \"api token 1\"}", []httpx.FieldError{
			{Field: "title", Message: "is required"},
		}},
	}
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		for _, tc := range tests {
			t.Run(method+tc.name, func(t *testing.T) {
				t.Parallel()
				path := "/api/notes"
				if method == http.MethodPut {
					path += "/1"
				}
				rec := testkit.Do(t, handler, method, path, strings.NewReader(tc.body))
				if rec.Code != http.StatusUnprocessableEntity {
					t.Fatalf("status %d, want 422: %s", rec.Code, rec.Body)
				}
				if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
					t.Fatalf("Content-Type %q, want application/problem+json", got)
				}
				problem := testkit.DecodeJSON[httpx.Problem](t, rec)
				if !reflect.DeepEqual(problem.Errors, tc.want) {
					t.Fatalf("errors %+v, want %+v", problem.Errors, tc.want)
				}
			})
		}
	}
}

// TestNotesChecksJSONBody checks the body before the handler decodes it.
func TestNotesChecksJSONBody(t *testing.T) {
	t.Parallel()
	valid := "{\"title\":\"title 1\",\"api_token\":\"api token 1\"}"
	oversized := valid + strings.Repeat(" ", 1<<20)
	tests := []struct {
		name, body    string
		status        int
		unknownLength bool
	}{
		{"valid", valid, http.StatusNoContent, false},
		{"valid without Content-Length", valid, http.StatusNoContent, true},
		{"at limit", valid + strings.Repeat(" ", (1<<20)-len(valid)), http.StatusNoContent, false},
		{"case variant", "{\"TITLE\":null}", http.StatusBadRequest, false},
		{"duplicate name", "{\"title\":null,\"title\":null}", http.StatusBadRequest, false},
		{"unknown member", "{\"id\":1}", http.StatusBadRequest, false},
		{"invalid UTF-8", "{\"title\":\"\xff\"}", http.StatusBadRequest, false},
		{"oversized", oversized, http.StatusRequestEntityTooLarge, false},
		{"oversized without Content-Length", oversized, http.StatusRequestEntityTooLarge, true},
	}
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		for _, tc := range tests {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				called := false
				next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					data, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatal(err)
					}
					if string(data) != tc.body {
						t.Fatal("the middleware changed the body")
					}
					var input gen.NoteInput
					if err := json.Unmarshal(data, &input); err != nil {
						t.Fatal(err)
					}
					w.WriteHeader(http.StatusNoContent)
				})
				mux := http.NewServeMux()
				mux.Handle("POST /api/notes", handlers.RequireFields(next))
				mux.Handle("PUT /api/notes/{id}", handlers.RequireFields(next))
				path := "/api/notes"
				if method == http.MethodPut {
					path += "/1"
				}
				req := httptest.NewRequest(method, path, strings.NewReader(tc.body))
				req.Header.Set("Content-Type", "application/json")
				if tc.unknownLength {
					req.ContentLength = -1
					req.TransferEncoding = []string{"chunked"}
				}
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				if rec.Code != tc.status {
					t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
				}
				if called != (tc.status == http.StatusNoContent) {
					t.Fatalf("handler called = %t", called)
				}
				if !called && rec.Header().Get("Content-Type") != "application/problem+json" {
					t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
				}
			})
		}
	}
}

// TestNotesResponseErrors checks the answers of ResponseError in server.go to
// errors that are not problems.
func TestNotesResponseErrors(t *testing.T) {
	t.Parallel()
	// A client that went away gets nothing: nobody reads the answer.
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	rec := httptest.NewRecorder()
	handlers.ResponseError(rec, httptest.NewRequestWithContext(canceled, http.MethodGet, "/api/notes", nil), fmt.Errorf("list: %w", context.Canceled))
	if rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
		t.Fatalf("canceled: status %d, body %q, want no answer", rec.Code, rec.Body)
	}

	// An expired request deadline is a 503 problem, not a 500.
	expired, stop := context.WithDeadline(t.Context(), time.Unix(0, 0))
	defer stop()
	rec = httptest.NewRecorder()
	handlers.ResponseError(rec, httptest.NewRequestWithContext(expired, http.MethodGet, "/api/notes", nil), fmt.Errorf("list: %w", context.DeadlineExceeded))
	if problem := testkit.DecodeJSON[httpx.Problem](t, rec); rec.Code != http.StatusServiceUnavailable || problem.Status != http.StatusServiceUnavailable {
		t.Fatalf("deadline: status %d, problem %+v, want 503", rec.Code, problem)
	}

	// Any other error is a 500 problem.
	rec = httptest.NewRecorder()
	handlers.ResponseError(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/notes", nil), errors.New("broken"))
	if problem := testkit.DecodeJSON[httpx.Problem](t, rec); rec.Code != http.StatusInternalServerError || problem.Detail != "" {
		t.Fatalf("other error: status %d, problem %+v, want a 500 without details", rec.Code, problem)
	}
}
func TestNotesRequiresUser(t *testing.T) {
	t.Parallel()
	handler := app.New(app.WithRoutes(func(mux *http.ServeMux) {
		routes.Register(mux, routes.Deps{})
	})).Handler()
	tests := []struct{ method, path string }{
		{http.MethodGet, "/api/notes"},
		{http.MethodPost, "/api/notes"},
		{http.MethodGet, "/api/notes/1"},
		{http.MethodPut, "/api/notes/1"},
		{http.MethodDelete, "/api/notes/1"},
		{http.MethodGet, "/api/notes/abc"},
		{http.MethodGet, "/api/notes?limit=bad"},
	}
	for _, tc := range tests {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			t.Parallel()
			rec := testkit.Do(t, handler, tc.method, tc.path, strings.NewReader("{}"))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401: %s", rec.Code, rec.Body)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
				t.Fatalf("Content-Type %q, want application/problem+json", got)
			}
			if problem := testkit.DecodeJSON[httpx.Problem](t, rec); problem.Status != http.StatusUnauthorized {
				t.Fatalf("problem = %+v", problem)
			}
		})
	}
	server := handlers.NewServer(nil)
	t.Run("strict List", func(t *testing.T) {
		response, err := server.ListNotes(t.Context(), gen.ListNotesRequestObject{})
		if err != nil {
			t.Fatal(err)
		}
		problem, ok := response.(gen.ListNotes401ApplicationProblemPlusJSONResponse)
		if !ok || problem.Status != http.StatusUnauthorized {
			t.Fatalf("response = %+v, want 401 problem", response)
		}
	})
	t.Run("strict Create", func(t *testing.T) {
		response, err := server.CreateNote(t.Context(), gen.CreateNoteRequestObject{})
		if err != nil {
			t.Fatal(err)
		}
		problem, ok := response.(gen.CreateNote401ApplicationProblemPlusJSONResponse)
		if !ok || problem.Status != http.StatusUnauthorized {
			t.Fatalf("response = %+v, want 401 problem", response)
		}
	})
	t.Run("strict Get", func(t *testing.T) {
		response, err := server.GetNote(t.Context(), gen.GetNoteRequestObject{})
		if err != nil {
			t.Fatal(err)
		}
		problem, ok := response.(gen.GetNote401ApplicationProblemPlusJSONResponse)
		if !ok || problem.Status != http.StatusUnauthorized {
			t.Fatalf("response = %+v, want 401 problem", response)
		}
	})
	t.Run("strict Update", func(t *testing.T) {
		response, err := server.UpdateNote(t.Context(), gen.UpdateNoteRequestObject{})
		if err != nil {
			t.Fatal(err)
		}
		problem, ok := response.(gen.UpdateNote401ApplicationProblemPlusJSONResponse)
		if !ok || problem.Status != http.StatusUnauthorized {
			t.Fatalf("response = %+v, want 401 problem", response)
		}
	})
	t.Run("strict Delete", func(t *testing.T) {
		response, err := server.DeleteNote(t.Context(), gen.DeleteNoteRequestObject{})
		if err != nil {
			t.Fatal(err)
		}
		problem, ok := response.(gen.DeleteNote401ApplicationProblemPlusJSONResponse)
		if !ok || problem.Status != http.StatusUnauthorized {
			t.Fatalf("response = %+v, want 401 problem", response)
		}
	})
}

func TestNotesRejectsForbiddenRoles(t *testing.T) {
	t.Parallel()
	handler := app.New(app.WithRoutes(func(mux *http.ServeMux) {
		routes.Register(mux, routes.Deps{})
	})).Handler()
	handler = authtest.AsRole(handler, 1, "__spinup_forbidden_role")
	tests := []struct{ method, path, body string }{
		{http.MethodGet, "/api/notes", ""},
		{http.MethodPost, "/api/notes", "{\"title\": \"title 1\", \"api_token\": \"api token 1\"}"},
		{http.MethodGet, "/api/notes/1", ""},
		{http.MethodPut, "/api/notes/1", "{\"title\": \"title 1\", \"api_token\": \"api token 1\"}"},
		{http.MethodDelete, "/api/notes/1", ""},
	}
	for _, tc := range tests {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			t.Parallel()
			rec := testkit.Do(t, handler, tc.method, tc.path, strings.NewReader(tc.body))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403: %s", rec.Code, rec.Body)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
				t.Fatalf("Content-Type %q, want application/problem+json", got)
			}
		})
	}
}
