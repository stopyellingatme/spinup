package handlers_test

import (
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"example.com/todo/api/db/migrations"
	"example.com/todo/api/internal/gen"
	"example.com/todo/api/internal/routes"
	"github.com/stopyellingatme/spinup/kit/app"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

func TestTodosCRUD(t *testing.T) {
	t.Parallel()
	pool := testkit.DBWithMigrations(t, migrations.FS)
	handler := app.New(app.WithRoutes(func(mux *http.ServeMux) {
		routes.Register(mux, routes.Deps{DB: pool})
	})).Handler()

	rec := testkit.Do(t, handler, http.MethodPost, "/api/todos", strings.NewReader(`{"title": "title 1", "done": true, "due": "2026-01-02", "priority": "low"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d, want %d\n%s", rec.Code, http.StatusCreated, rec.Body)
	}
	created := testkit.DecodeJSON[gen.Todo](t, rec)
	path := fmt.Sprintf("/api/todos/%d", created.Id)

	rec = testkit.Do(t, handler, http.MethodGet, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: status %d, want %d\n%s", rec.Code, http.StatusOK, rec.Body)
	}
	if got := testkit.DecodeJSON[gen.Todo](t, rec); !reflect.DeepEqual(got, created) {
		t.Fatalf("get: got %+v, want %+v", got, created)
	}

	rec = testkit.Do(t, handler, http.MethodGet, "/api/todos?limit=10", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: status %d, want %d\n%s", rec.Code, http.StatusOK, rec.Body)
	}
	list := testkit.DecodeJSON[gen.TodoList](t, rec)
	if list.Total != 1 || len(list.Items) != 1 || !reflect.DeepEqual(list.Items[0], created) {
		t.Fatalf("list: got %+v, want one item %+v", list, created)
	}

	rec = testkit.Do(t, handler, http.MethodPut, path, strings.NewReader(`{"title": "title 2", "done": false, "due": null, "priority": "high"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status %d, want %d\n%s", rec.Code, http.StatusOK, rec.Body)
	}
	updated := testkit.DecodeJSON[gen.Todo](t, rec)
	if updated.Id != created.Id || reflect.DeepEqual(updated.Title, created.Title) {
		t.Fatalf("update: got %+v, want a new title", updated)
	}
	rec = testkit.Do(t, handler, http.MethodGet, path, nil)
	if got := testkit.DecodeJSON[gen.Todo](t, rec); !reflect.DeepEqual(got, updated) {
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
}

func TestTodosRejectsBadInput(t *testing.T) {
	t.Parallel()
	// These requests fail before a query runs, so they need no database.
	handler := app.New(app.WithRoutes(func(mux *http.ServeMux) {
		routes.Register(mux, routes.Deps{})
	})).Handler()
	tests := []struct {
		name, method, path, body string
		status                   int
	}{
		{"wrong type", http.MethodPost, "/api/todos", `{"title": 42}`, http.StatusUnprocessableEntity},
		{"not JSON", http.MethodPost, "/api/todos", `{`, http.StatusBadRequest},
		{"bad id", http.MethodGet, "/api/todos/abc", "", http.StatusBadRequest},
		{"limit too large", http.MethodGet, "/api/todos?limit=201", "", http.StatusUnprocessableEntity},
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
