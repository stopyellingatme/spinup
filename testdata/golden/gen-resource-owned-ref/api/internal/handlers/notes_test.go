package handlers_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	kitmigrations "example.com/todo/api/db/kit"
	"example.com/todo/api/db/migrations"
	"example.com/todo/api/internal/gen"
	"example.com/todo/api/internal/handlers"
	"example.com/todo/api/internal/routes"
	"github.com/stopyellingatme/spinup/kit/app"
	"github.com/stopyellingatme/spinup/kit/auth/authtest"
	"github.com/stopyellingatme/spinup/kit/httpx"
	"github.com/stopyellingatme/spinup/kit/testkit"
)

func TestNotesCRUD(t *testing.T) {
	t.Parallel()
	pool := testkit.DBWithMigrations(t, migrations.FS, testkit.WithKitMigrations(kitmigrations.FS))
	handler := app.New(app.WithRoutes(func(mux *http.ServeMux) {
		routes.Register(mux, routes.Deps{DB: pool})
	})).Handler()
	var userA int64
	if err := pool.QueryRow(t.Context(), `INSERT INTO users (email, password_hash) VALUES ('owner-a@example.com', 'test-hash-a') RETURNING id`).Scan(&userA); err != nil {
		t.Fatalf("insert user A: %v", err)
	}
	var userB int64
	if err := pool.QueryRow(t.Context(), `INSERT INTO users (email, password_hash) VALUES ('owner-b@example.com', 'test-hash-b') RETURNING id`).Scan(&userB); err != nil {
		t.Fatalf("insert user B: %v", err)
	}
	other := authtest.As(handler, userB)
	handler = authtest.As(handler, userA)

	// Rows that the notes refer to.
	var projectID int64
	if err := pool.QueryRow(t.Context(), `INSERT INTO projects (user_id, name) VALUES ($1, 'sample') RETURNING id`, userA).Scan(&projectID); err != nil {
		t.Fatalf("insert a row into projects: %v", err)
	}

	// Rows of user B. Owner keys need rows of the same user.
	var otherProjectID int64
	if err := pool.QueryRow(t.Context(), `INSERT INTO projects (user_id, name) VALUES ($1, 'sample') RETURNING id`, userB).Scan(&otherProjectID); err != nil {
		t.Fatalf("insert a row into projects: %v", err)
	}

	rec := testkit.Do(t, handler, http.MethodPost, "/api/notes", strings.NewReader(fmt.Sprintf(`{"title": "title 1", "project_id": %d, "parent_id": null}`, projectID)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d, want %d\n%s", rec.Code, http.StatusCreated, rec.Body)
	}
	created := testkit.DecodeJSON[gen.Note](t, rec)
	path := fmt.Sprintf("/api/notes/%d", created.Id)
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		var body io.Reader
		if method == http.MethodPut {
			body = strings.NewReader(fmt.Sprintf(`{"title": "title 2", "project_id": %d, "parent_id": null}`, projectID))
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

	// User B cannot refer to rows of user A. Such a row gets the same answer
	// as a row that does not exist, so the answer does not show that it exists.
	rec = testkit.Do(t, other, http.MethodPost, "/api/notes", strings.NewReader(fmt.Sprintf(`{"title": "title 1", "project_id": %d, "parent_id": null}`, otherProjectID)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("other user create: status %d: %s", rec.Code, rec.Body)
	}
	otherPath := fmt.Sprintf("/api/notes/%d", testkit.DecodeJSON[gen.Note](t, rec).Id)
	for _, tc := range []struct{ name, method, path, body, field string }{
		{"create with project_id of user A", http.MethodPost, "/api/notes", fmt.Sprintf(`{"title": "title 1", "project_id": %d, "parent_id": null}`, projectID), "project_id"},
		{"create with a missing project_id", http.MethodPost, "/api/notes", fmt.Sprintf(`{"title": "title 1", "project_id": %d, "parent_id": null}`, int64(-1)), "project_id"},
		{"update with project_id of user A", http.MethodPut, otherPath, fmt.Sprintf(`{"title": "title 2", "project_id": %d, "parent_id": null}`, projectID), "project_id"},
		{"create with parent_id of user A", http.MethodPost, "/api/notes", fmt.Sprintf(`{"title": "title 1", "project_id": %d, "parent_id": %d}`, otherProjectID, created.Id), "parent_id"},
		{"create with a missing parent_id", http.MethodPost, "/api/notes", fmt.Sprintf(`{"title": "title 1", "project_id": %d, "parent_id": %d}`, otherProjectID, int64(-1)), "parent_id"},
		{"update with parent_id of user A", http.MethodPut, otherPath, fmt.Sprintf(`{"title": "title 2", "project_id": %d, "parent_id": %d}`, otherProjectID, created.Id), "parent_id"},
	} {
		rec := testkit.Do(t, other, tc.method, tc.path, strings.NewReader(tc.body))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status %d, want 422: %s", tc.name, rec.Code, rec.Body)
		}
		problem := testkit.DecodeJSON[httpx.Problem](t, rec)
		if want := []httpx.FieldError{{Field: tc.field, Message: "must refer to an existing record"}}; !reflect.DeepEqual(problem.Errors, want) {
			t.Fatalf("%s: errors %+v, want %+v", tc.name, problem.Errors, want)
		}
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

	rec = testkit.Do(t, handler, http.MethodPut, path, strings.NewReader(fmt.Sprintf(`{"title": "title 2", "project_id": %d, "parent_id": null}`, projectID)))
	if rec.Code != http.StatusOK {
		t.Fatalf("update: status %d, want %d\n%s", rec.Code, http.StatusOK, rec.Body)
	}
	updated := testkit.DecodeJSON[gen.Note](t, rec)
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

	// User A can still delete the rows that the notes referred to.
	if tag, err := pool.Exec(t.Context(), `DELETE FROM projects WHERE id = $1`, projectID); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("delete the projects row of user A: %v", err)
	}
}

func TestNotesRejectsBadInput(t *testing.T) {
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

func TestNotesRequiresFields(t *testing.T) {
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
			{Field: "project_id", Message: "is required"},
			{Field: "title", Message: "is required"},
		}},
		{"missing project_id", "{\"title\": \"title 1\", \"parent_id\": null}", []httpx.FieldError{
			{Field: "project_id", Message: "is required"},
		}},
		{"null project_id", "{\"title\": \"title 1\", \"project_id\": null, \"parent_id\": null}", []httpx.FieldError{
			{Field: "project_id", Message: "is required"},
		}},
		{"missing title", "{\"project_id\": 1, \"parent_id\": null}", []httpx.FieldError{
			{Field: "title", Message: "is required"},
		}},
		{"null title", "{\"title\": null, \"project_id\": 1, \"parent_id\": null}", []httpx.FieldError{
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
	valid := "{\"title\":\"title 1\",\"project_id\":1,\"parent_id\":null}"
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
