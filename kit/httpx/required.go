package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
)

// RequireJSONFields checks that each required key exists and is not null.
// Missing or null keys produce a 422 Error with field errors sorted by name.
// Invalid JSON produces 400. A non-object value produces 422.
// The body remains readable and retains its original Close method.
// Use MaxBody before this check to limit the body size.
func RequireJSONFields(r *http.Request, required []string) error {
	if len(required) == 0 {
		return nil
	}
	if r.Body == nil {
		return classifyDecodeError(io.EOF)
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return classifyDecodeError(err)
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		var typ *json.UnmarshalTypeError
		if errors.As(err, &typ) {
			return decodeError(http.StatusUnprocessableEntity, "The request body must be a JSON object.")
		}
		return classifyDecodeError(err)
	}
	var errs []FieldError
	for _, key := range required {
		value, ok := values[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			errs = append(errs, FieldError{Field: key, Message: "is required"})
		}
	}
	if len(errs) > 0 {
		sort.Slice(errs, func(i, j int) bool { return errs[i].Field < errs[j].Field })
		return &Error{Problem: Problem{
			Status: http.StatusUnprocessableEntity,
			Detail: "The request has invalid values.",
			Errors: errs,
		}}
	}
	r.Body = struct {
		io.Reader
		io.Closer
	}{bytes.NewReader(data), r.Body}
	return nil
}
