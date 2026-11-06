package httpx

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"slices"
	"sort"
)

// BodyRules defines the top-level fields allowed in a JSON request object.
type BodyRules struct {
	// Required lists fields that must exist and must not be null.
	Required []string
	// Known lists allowed fields. An empty list allows any field name.
	Known []string
}

// CheckJSONBody validates one JSON object with exact field names and the given rules.
// It rejects duplicate names at any depth and invalid UTF-8 with status 400.
// Non-object values and missing or null required fields produce status 422.
// Field errors are sorted by name. An empty body with empty rules is allowed.
//
// The body remains readable and retains its original Close method, including on errors.
// Use MaxBody before this check to impose a size limit, which produces status 413.
func CheckJSONBody(r *http.Request, rules BodyRules) error {
	if r.Body == nil {
		if len(rules.Required) == 0 && len(rules.Known) == 0 {
			return nil
		}
		return classifyDecodeError(io.EOF)
	}
	// Record consumed bytes so every return path preserves the original body.
	body := r.Body
	var data bytes.Buffer
	defer func() {
		r.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(&data, body), body}
	}()
	decoder := takeJSONDecoder(io.TeeReader(body, &data))
	defer putJSONDecoder(decoder)
	kind := decoder.PeekKind()
	present := make([]bool, len(rules.Required))
	unknown := false
	if kind == '{' {
		if _, err := decoder.ReadToken(); err != nil {
			return classifyDecodeError(err)
		}
		for decoder.PeekKind() != '}' {
			name, err := decoder.ReadToken()
			if err != nil {
				return classifyDecodeError(err)
			}
			key := name.String()
			if len(rules.Known) > 0 && !slices.Contains(rules.Known, key) {
				unknown = true
			}
			nonnull := decoder.PeekKind() != 'n'
			if err := decoder.SkipValue(); err != nil {
				return classifyDecodeError(err)
			}
			if nonnull {
				for i, required := range rules.Required {
					if key == required {
						present[i] = true
					}
				}
			}
		}
		if _, err := decoder.ReadToken(); err != nil {
			return classifyDecodeError(err)
		}
	} else if err := decoder.SkipValue(); err != nil {
		if errors.Is(err, io.EOF) && len(rules.Required) == 0 && len(rules.Known) == 0 {
			return nil
		}
		return classifyDecodeError(err)
	}

	// Validate trailing input before reporting schema or root-kind failures.
	if err := decoder.SkipValue(); !errors.Is(err, io.EOF) {
		if err != nil {
			return classifyDecodeError(err)
		}
		return decodeError(http.StatusBadRequest, "The request body must contain only one JSON value.")
	}
	if kind != '{' {
		return decodeError(http.StatusUnprocessableEntity, "The request body must be a JSON object.")
	}
	if unknown {
		return decodeError(http.StatusBadRequest, invalidJSONDetail)
	}
	var errs []FieldError
	for i, key := range rules.Required {
		if !present[i] {
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
	return nil
}

// RequireJSONFields validates a JSON object and its required fields with CheckJSONBody.
// It validates syntax even when required is empty. Unknown field names are allowed.
func RequireJSONFields(r *http.Request, required []string) error {
	return CheckJSONBody(r, BodyRules{Required: required})
}
