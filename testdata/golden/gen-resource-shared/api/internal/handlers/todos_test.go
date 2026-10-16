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
	"example.com/todo/api/internal/handlers"
	"example.com/todo/api/internal/routes"
	"github.com/stopyellingatme/spinup/kit/app"
	"github.com/stopyellingatme/spinup/kit/auth/authtest"
	"github.com/stopyellingatme/spinup/kit/httpx"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

func TestTodosCRUD(t *testing.T) {
	t.Parallel()
	pool := testkit.DBWithMigrations(t, migrations.FS)
	handler := app.New(app.WithRoutes(func(mux *http.ServeMux) {
		routes.Register(mux, routes.Deps{DB: pool})
	})).Handler()
	var userA int64
	if err := pool.QueryRow(t.Context(), `INSERT INTO users (email, password_hash) VALUES ('owner-a@example.com', 'test-hash-a') RETURNING id`).Scan(&userA); err != nil {
		t.Fatalf("insert user A: %v", err)
	}
	handler = authtest.As(handler, userA)

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
	handler = authtest.As(handler, 1)
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

func TestTodosRequiresFields(t *testing.T) {
	t.Parallel()
	handler := app.New(app.WithRoutes(func(mux *http.ServeMux) {
		routes.Register(mux, routes.Deps{})
	})).Handler()
	handler = authtest.As(handler, 1)
	tests := []struct {
		name, body string
		want       []httpx.FieldError
	}{
		{"missing all", "{}", []httpx.FieldError{
			{Field: "done", Message: "is required"},
			{Field: "priority", Message: "is required"},
			{Field: "title", Message: "is required"},
		}},
		{"body null", "null", []httpx.FieldError{
			{Field: "done", Message: "is required"},
			{Field: "priority", Message: "is required"},
			{Field: "title", Message: "is required"},
		}},
		{"missing done", "{\"title\": \"title 1\", \"due\": \"2026-01-02\", \"priority\": \"low\"}", []httpx.FieldError{
			{Field: "done", Message: "is required"},
		}},
		{"null done", "{\"title\": \"title 1\", \"done\": null, \"due\": \"2026-01-02\", \"priority\": \"low\"}", []httpx.FieldError{
			{Field: "done", Message: "is required"},
		}},
		{"missing priority", "{\"title\": \"title 1\", \"done\": true, \"due\": \"2026-01-02\"}", []httpx.FieldError{
			{Field: "priority", Message: "is required"},
		}},
		{"null priority", "{\"title\": \"title 1\", \"done\": true, \"due\": \"2026-01-02\", \"priority\": null}", []httpx.FieldError{
			{Field: "priority", Message: "is required"},
		}},
		{"missing title", "{\"done\": true, \"due\": \"2026-01-02\", \"priority\": \"low\"}", []httpx.FieldError{
			{Field: "title", Message: "is required"},
		}},
		{"null title", "{\"title\": null, \"done\": true, \"due\": \"2026-01-02\", \"priority\": \"low\"}", []httpx.FieldError{
			{Field: "title", Message: "is required"},
		}},
	}
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		for _, tc := range tests {
			t.Run(method+tc.name, func(t *testing.T) {
				t.Parallel()
				path := "/api/todos"
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
func TestTodosRequiresUser(t *testing.T) {
	t.Parallel()
	handler := app.New(app.WithRoutes(func(mux *http.ServeMux) {
		routes.Register(mux, routes.Deps{})
	})).Handler()
	tests := []struct{ method, path string }{
		{http.MethodGet, "/api/todos"},
		{http.MethodPost, "/api/todos"},
		{http.MethodGet, "/api/todos/1"},
		{http.MethodPut, "/api/todos/1"},
		{http.MethodDelete, "/api/todos/1"},
		{http.MethodGet, "/api/todos/abc"},
		{http.MethodGet, "/api/todos?limit=bad"},
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
		response, err := server.ListTodos(t.Context(), gen.ListTodosRequestObject{})
		if err != nil {
			t.Fatal(err)
		}
		problem, ok := response.(gen.ListTodos401ApplicationProblemPlusJSONResponse)
		if !ok || problem.Status != http.StatusUnauthorized {
			t.Fatalf("response = %+v, want 401 problem", response)
		}
	})
	t.Run("strict Create", func(t *testing.T) {
		response, err := server.CreateTodo(t.Context(), gen.CreateTodoRequestObject{})
		if err != nil {
			t.Fatal(err)
		}
		problem, ok := response.(gen.CreateTodo401ApplicationProblemPlusJSONResponse)
		if !ok || problem.Status != http.StatusUnauthorized {
			t.Fatalf("response = %+v, want 401 problem", response)
		}
	})
	t.Run("strict Get", func(t *testing.T) {
		response, err := server.GetTodo(t.Context(), gen.GetTodoRequestObject{})
		if err != nil {
			t.Fatal(err)
		}
		problem, ok := response.(gen.GetTodo401ApplicationProblemPlusJSONResponse)
		if !ok || problem.Status != http.StatusUnauthorized {
			t.Fatalf("response = %+v, want 401 problem", response)
		}
	})
	t.Run("strict Update", func(t *testing.T) {
		response, err := server.UpdateTodo(t.Context(), gen.UpdateTodoRequestObject{})
		if err != nil {
			t.Fatal(err)
		}
		problem, ok := response.(gen.UpdateTodo401ApplicationProblemPlusJSONResponse)
		if !ok || problem.Status != http.StatusUnauthorized {
			t.Fatalf("response = %+v, want 401 problem", response)
		}
	})
	t.Run("strict Delete", func(t *testing.T) {
		response, err := server.DeleteTodo(t.Context(), gen.DeleteTodoRequestObject{})
		if err != nil {
			t.Fatal(err)
		}
		problem, ok := response.(gen.DeleteTodo401ApplicationProblemPlusJSONResponse)
		if !ok || problem.Status != http.StatusUnauthorized {
			t.Fatalf("response = %+v, want 401 problem", response)
		}
	})
}
