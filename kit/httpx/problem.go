package httpx

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"sync"
)

// FieldError describes a validation failure for one input field.
type FieldError struct {
	// Field identifies the invalid input field.
	Field string `json:"field"`
	// Message describes the field validation failure.
	Message string `json:"message"`
}

// Problem describes an HTTP error using RFC 9457 problem details.
type Problem struct {
	// Type identifies the problem type with a URI reference.
	Type string `json:"type"`
	// Title summarizes the problem type.
	Title string `json:"title"`
	// Status contains the HTTP status code.
	Status int `json:"status"`
	// Detail explains this occurrence without exposing internal information.
	Detail string `json:"detail,omitempty"`
	// Instance identifies this occurrence with a URI reference.
	Instance string `json:"instance,omitempty"`
	// Errors contains validation failures for individual fields.
	Errors []FieldError `json:"errors,omitempty"`
}

// Error carries a problem that is safe to return to an HTTP client.
type Error struct {
	// Problem contains the public error details.
	Problem Problem
}

// Error returns the problem title and detail without adding internal error text.
func (e *Error) Error() string {
	title := e.Problem.Title
	if title == "" {
		title = http.StatusText(e.Problem.Status)
	}
	if e.Problem.Detail != "" {
		return title + ": " + e.Problem.Detail
	}
	return title
}

// ProblemFrom preserves a wrapped Error. Other errors become a generic 500 problem.
func ProblemFrom(err error) Problem {
	var problem *Error
	if errors.As(err, &problem) && problem != nil {
		return problem.Problem
	}
	return Problem{Status: http.StatusInternalServerError}
}

// WriteProblem writes application/problem+json, including the context request ID.
// Missing status, type, and title default to 500, about:blank, and the status text.
func WriteProblem(w http.ResponseWriter, r *http.Request, p Problem) {
	if p.Status == 0 {
		p.Status = http.StatusInternalServerError
	}
	if p.Type == "" {
		p.Type = "about:blank"
	}
	if p.Title == "" {
		p.Title = http.StatusText(p.Status)
	}
	body := struct {
		// Problem contains the public error details.
		Problem
		RequestID string `json:"request_id,omitempty"`
	}{Problem: p, RequestID: RequestIDFrom(r.Context())}
	// The payload contains only JSON-compatible fields, so marshaling cannot fail.
	data, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_, _ = w.Write(append(data, '\n'))
}

// WriteJSON encodes v before writing the status and application/json response.
// An encoding failure leaves the response untouched. Write errors are returned.
func WriteJSON(w http.ResponseWriter, status int, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, err = w.Write(append(data, '\n'))
	return err
}

// DecodeJSON reads exactly one JSON value into dst with exact field names.
// It rejects unknown fields, duplicate names, and invalid UTF-8.
// It returns an Error with a stable message and status 400, or 413 for size errors.
// Use MaxBody to impose a request size limit.
func DecodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return decodeError(http.StatusBadRequest, "The request body must contain one JSON value.")
	}
	decoder := takeJSONDecoder(r.Body)
	defer putJSONDecoder(decoder)
	if err := jsonv2.UnmarshalDecode(decoder, dst, jsonv2.RejectUnknownMembers(true)); err != nil {
		return classifyDecodeError(err)
	}
	if err := decoder.SkipValue(); !errors.Is(err, io.EOF) {
		if err != nil {
			return classifyDecodeError(err)
		}
		return decodeError(http.StatusBadRequest, "The request body must contain only one JSON value.")
	}
	return nil
}

const invalidJSONDetail = "The request body must contain valid JSON with only known fields and correct value types."

func classifyDecodeError(err error) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return decodeError(http.StatusRequestEntityTooLarge, "The request body exceeds the size limit.")
	}
	if errors.Is(err, io.EOF) {
		return decodeError(http.StatusBadRequest, "The request body must contain one JSON value.")
	}
	// Keep parser details private. Their wording can change between Go releases.
	return decodeError(http.StatusBadRequest, invalidJSONDetail)
}
func decodeError(status int, detail string) *Error {
	return &Error{Problem: Problem{Status: status, Detail: detail}}
}

// Keep the common request path close to UnmarshalRead, which also pools decoders.
var jsonDecoders = sync.Pool{New: func() any {
	return jsontext.NewDecoder(http.NoBody)
}}

func takeJSONDecoder(body io.Reader) *jsontext.Decoder {
	decoder := jsonDecoders.Get().(*jsontext.Decoder)
	decoder.Reset(body)
	return decoder
}

func putJSONDecoder(decoder *jsontext.Decoder) {
	// Do not retain buffers from unusually large requests in the pool.
	if decoder.InputOffset()+int64(len(decoder.UnreadBuffer())) > 64*1024 {
		return
	}
	// Release the request reader before returning the decoder to the pool.
	decoder.Reset(http.NoBody)
	jsonDecoders.Put(decoder)
}
