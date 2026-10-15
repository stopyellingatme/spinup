package resource

import (
	"go/format"
	"strings"
)

// serverGo is api/internal/handlers/server.go. The first resource writes it,
// and the application owns it after that.
const serverGo = `// Package handlers implements the API operations in api/openapi/.
package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"MODULE/internal/gen"
	"MODULE/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stopyellingatme/spinup/kit/httpx"
)

// Server implements the operations of gen.StrictServerInterface. Each
// resource adds its methods in its own file.
type Server struct {
	// Queries runs the queries in api/db/queries.
	Queries *store.Queries
	// DB is the pool behind Queries. Use it for transactions.
	DB *pgxpool.Pool
}

// NewServer returns a Server that runs its queries on pool.
func NewServer(pool *pgxpool.Pool) *Server {
	return &Server{Queries: store.New(pool), DB: pool}
}

var _ gen.StrictServerInterface = (*Server)(nil)

// The page size limits of the list operations.
const (
	defaultLimit = 50
	maxLimit     = 200
)

// errNotFound is the error of an operation on a row that does not exist.
var errNotFound = &httpx.Error{Problem: httpx.Problem{Status: http.StatusNotFound, Detail: "The record does not exist."}}

// invalid returns a 422 problem with the field errors.
func invalid(errs []httpx.FieldError) error {
	return &httpx.Error{Problem: httpx.Problem{
		Status: http.StatusUnprocessableEntity,
		Detail: "The request has invalid values.",
		Errors: errs,
	}}
}

// page validates the limit and offset query parameters and applies their
// defaults.
func page(limit, offset *int32) (int32, int32, error) {
	l, o := int32(defaultLimit), int32(0)
	var errs []httpx.FieldError
	if limit != nil {
		l = *limit
		if l < 1 || l > maxLimit {
			errs = append(errs, httpx.FieldError{Field: "limit", Message: "must be from 1 to 200"})
		}
	}
	if offset != nil {
		o = *offset
		if o < 0 {
			errs = append(errs, httpx.FieldError{Field: "offset", Message: "must not be negative"})
		}
	}
	if len(errs) > 0 {
		return 0, 0, invalid(errs)
	}
	return l, o, nil
}

// RequestError writes the problem for a request that the generated code
// cannot read: a bad parameter or a bad JSON body.
func RequestError(w http.ResponseWriter, r *http.Request, err error) {
	httpx.WriteProblem(w, r, requestProblem(err))
}

func requestProblem(err error) httpx.Problem {
	var (
		format   *gen.InvalidParamFormatError
		required *gen.RequiredParamError
		syntax   *json.SyntaxError
		typ      *json.UnmarshalTypeError
		tooLarge *http.MaxBytesError
	)
	switch {
	case errors.As(err, &tooLarge):
		return httpx.Problem{Status: http.StatusRequestEntityTooLarge, Detail: "The request body is too large."}
	case errors.As(err, &format):
		return httpx.Problem{Status: http.StatusBadRequest, Detail: "A parameter has an invalid format.",
			Errors: []httpx.FieldError{{Field: format.ParamName, Message: "has an invalid format"}}}
	case errors.As(err, &required):
		return httpx.Problem{Status: http.StatusBadRequest, Detail: "A parameter is missing.",
			Errors: []httpx.FieldError{{Field: required.ParamName, Message: "is required"}}}
	case errors.As(err, &syntax), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return httpx.Problem{Status: http.StatusBadRequest, Detail: "The request body must be one valid JSON value."}
	case errors.As(err, &typ):
		return httpx.Problem{Status: http.StatusUnprocessableEntity, Detail: "The request has invalid values.",
			Errors: []httpx.FieldError{{Field: typ.Field, Message: "has the wrong type"}}}
	case strings.HasPrefix(err.Error(), "can't decode JSON body"):
		// A value with the wrong format, such as a bad date or UUID.
		return httpx.Problem{Status: http.StatusUnprocessableEntity, Detail: "The request has a value with an invalid format."}
	}
	return httpx.Problem{Status: http.StatusBadRequest, Detail: "The request is not valid."}
}

// ResponseError writes the problem for an error that an operation returns.
// It logs the errors that become a 500 response.
func ResponseError(w http.ResponseWriter, r *http.Request, err error) {
	p := responseProblem(r, err)
	if p.Status >= http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	}
	httpx.WriteProblem(w, r, p)
}

func responseProblem(r *http.Request, err error) httpx.Problem {
	var problem *httpx.Error
	if errors.As(err, &problem) {
		return problem.Problem
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return errNotFound.Problem
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == "23505": // unique_violation
			return httpx.Problem{Status: http.StatusConflict, Detail: "A record with the same unique value already exists."}
		case pgErr.Code == "23503" && r.Method == http.MethodDelete: // foreign_key_violation
			return httpx.Problem{Status: http.StatusConflict, Detail: "Other records refer to this record."}
		case pgErr.Code == "23503":
			return httpx.Problem{Status: http.StatusUnprocessableEntity, Detail: "A referenced record does not exist."}
		case pgErr.Code == "23502", pgErr.Code == "23514": // not_null_violation, check_violation
			return httpx.Problem{Status: http.StatusUnprocessableEntity, Detail: "A value breaks a rule of the database."}
		case strings.HasPrefix(pgErr.Code, "22"): // data_exception
			return httpx.Problem{Status: http.StatusUnprocessableEntity, Detail: "A value has an invalid format or is out of range."}
		}
	}
	return httpx.Problem{Status: http.StatusInternalServerError}
}
`

// routesGenGo is api/internal/routes/routes_gen.go once the application has
// a resource. Its content does not depend on the resources (ADR 0007 §3).
const routesGenGo = `// Code generated by spinup. DO NOT EDIT.

package routes

import (
	"net/http"

	"MODULE/internal/gen"
	"MODULE/internal/handlers"
)

// registerGenerated adds the operations in api/openapi/ to mux. Errors become
// problem details (RFC 9457).
func registerGenerated(mux *http.ServeMux, d Deps) {
	strict := gen.NewStrictHandlerWithOptions(handlers.NewServer(d.DB), nil, gen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  handlers.RequestError,
		ResponseErrorHandlerFunc: handlers.ResponseError,
	})
	gen.HandlerWithOptions(strict, gen.StdHTTPServerOptions{
		BaseRouter:       mux,
		ErrorHandlerFunc: handlers.RequestError,
	})
}
`

// withModule puts the api module path into src and formats it.
func withModule(src, module string) ([]byte, error) {
	return format.Source([]byte(strings.ReplaceAll(src, "MODULE", module)))
}
