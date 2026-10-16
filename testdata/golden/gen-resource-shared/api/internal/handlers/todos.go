package handlers

import (
	"context"
	"net/http"

	"example.com/todo/api/internal/gen"
	"example.com/todo/api/internal/store"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/httpx"
)

func init() {
	required := []string{
		"title",
		"done",
		"priority",
	}
	requiredFields["POST /api/todos"] = required
	requiredFields["PUT /api/todos/{id}"] = required
}

// todoFromStore converts a store row to the API type.
func todoFromStore(row store.Todo) gen.Todo {
	item := gen.Todo{
		Id:        row.ID,
		Title:     row.Title,
		Done:      row.Done,
		Priority:  gen.TodoPriority(row.Priority),
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
	if row.Due != nil {
		item.Due = &openapi_types.Date{Time: *row.Due}
	}
	return item
}

// todoParams validates an API input and converts it to store values.
func todoParams(in gen.TodoInput) (store.CreateTodoParams, error) {
	var errs []httpx.FieldError
	params := store.CreateTodoParams{
		Title:    in.Title,
		Done:     in.Done,
		Priority: string(in.Priority),
	}
	if in.Due != nil {
		params.Due = &in.Due.Time
	}
	if !in.Priority.Valid() {
		errs = append(errs, httpx.FieldError{Field: "priority", Message: "must be one of: low, normal, high"})
	}
	if len(errs) > 0 {
		return store.CreateTodoParams{}, invalid(errs)
	}
	return params, nil
}

// ListTodos returns one page of todos in id order, and the total count.
func (s *Server) ListTodos(ctx context.Context, req gen.ListTodosRequestObject) (gen.ListTodosResponseObject, error) {
	_, ok := auth.UserFrom(ctx)
	if !ok {
		detail := "A signed-in user is required."
		return gen.ListTodos401ApplicationProblemPlusJSONResponse{Type: "about:blank", Status: http.StatusUnauthorized, Title: http.StatusText(http.StatusUnauthorized), Detail: &detail}, nil
	}
	limit, offset, err := page(req.Params.Limit, req.Params.Offset)
	if err != nil {
		return nil, err
	}
	rows, err := s.Queries.ListTodos(ctx, store.ListTodosParams{RowLimit: limit, RowOffset: offset})
	if err != nil {
		return nil, err
	}
	total, err := s.Queries.CountTodos(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]gen.Todo, 0, len(rows))
	for _, row := range rows {
		items = append(items, todoFromStore(row))
	}
	return gen.ListTodos200JSONResponse{Items: items, Total: total}, nil
}

// CreateTodo inserts a todo.
func (s *Server) CreateTodo(ctx context.Context, req gen.CreateTodoRequestObject) (gen.CreateTodoResponseObject, error) {
	_, ok := auth.UserFrom(ctx)
	if !ok {
		detail := "A signed-in user is required."
		return gen.CreateTodo401ApplicationProblemPlusJSONResponse{Type: "about:blank", Status: http.StatusUnauthorized, Title: http.StatusText(http.StatusUnauthorized), Detail: &detail}, nil
	}
	params, err := todoParams(*req.Body)
	if err != nil {
		return nil, err
	}
	row, err := s.Queries.CreateTodo(ctx, params)
	if err != nil {
		return nil, err
	}
	return gen.CreateTodo201JSONResponse(todoFromStore(row)), nil
}

// GetTodo returns one todo.
func (s *Server) GetTodo(ctx context.Context, req gen.GetTodoRequestObject) (gen.GetTodoResponseObject, error) {
	_, ok := auth.UserFrom(ctx)
	if !ok {
		detail := "A signed-in user is required."
		return gen.GetTodo401ApplicationProblemPlusJSONResponse{Type: "about:blank", Status: http.StatusUnauthorized, Title: http.StatusText(http.StatusUnauthorized), Detail: &detail}, nil
	}
	row, err := s.Queries.GetTodo(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	return gen.GetTodo200JSONResponse(todoFromStore(row)), nil
}

// UpdateTodo replaces every writable field of a todo.
func (s *Server) UpdateTodo(ctx context.Context, req gen.UpdateTodoRequestObject) (gen.UpdateTodoResponseObject, error) {
	_, ok := auth.UserFrom(ctx)
	if !ok {
		detail := "A signed-in user is required."
		return gen.UpdateTodo401ApplicationProblemPlusJSONResponse{Type: "about:blank", Status: http.StatusUnauthorized, Title: http.StatusText(http.StatusUnauthorized), Detail: &detail}, nil
	}
	params, err := todoParams(*req.Body)
	if err != nil {
		return nil, err
	}
	row, err := s.Queries.UpdateTodo(ctx, store.UpdateTodoParams{
		ID:       req.Id,
		Title:    params.Title,
		Done:     params.Done,
		Due:      params.Due,
		Priority: params.Priority,
	})
	if err != nil {
		return nil, err
	}
	return gen.UpdateTodo200JSONResponse(todoFromStore(row)), nil
}

// DeleteTodo deletes a todo.
func (s *Server) DeleteTodo(ctx context.Context, req gen.DeleteTodoRequestObject) (gen.DeleteTodoResponseObject, error) {
	_, ok := auth.UserFrom(ctx)
	if !ok {
		detail := "A signed-in user is required."
		return gen.DeleteTodo401ApplicationProblemPlusJSONResponse{Type: "about:blank", Status: http.StatusUnauthorized, Title: http.StatusText(http.StatusUnauthorized), Detail: &detail}, nil
	}
	deleted, err := s.Queries.DeleteTodo(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if deleted == 0 {
		return nil, errNotFound
	}
	return gen.DeleteTodo204Response{}, nil
}
