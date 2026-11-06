package logx_test

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stopyellingatme/spinup/kit/logx"
)

type protectedText string

func (protectedText) MarshalText() ([]byte, error) { return []byte("MASKED"), nil }

type protectedString string

func (protectedString) String() string { return "MASKED" }

type protectedError string

func (protectedError) Error() string { return "MASKED" }

type protectedLogValue string

func (protectedLogValue) LogValue() slog.Value { return slog.StringValue("MASKED") }

type urlText string

func (s urlText) MarshalText() ([]byte, error) {
	return []byte("postgres://user:" + string(s) + "@localhost/db"), nil
}

type invalidText string

func (invalidText) MarshalText() ([]byte, error) { return nil, errors.New("cannot serialize") }

func TestCustomMasking(t *testing.T) {
	const secret = "private-credential-123"
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"text", protectedText(secret), "MASKED"},
		{"stringer", protectedString(secret), "MASKED"},
		{"error", protectedError(secret), "MASKED"},
		{"logvaluer", protectedLogValue(secret), "MASKED"},
		{"serialized URL", urlText(secret), "postgres://user:****@localhost/db"},
		{"serialization error", invalidText(secret), "****"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, nested := range []bool{false, true} {
				// slog resolves LogValuer only on top-level attributes.
				if _, ok := tc.value.(slog.LogValuer); nested && ok {
					continue
				}
				var out bytes.Buffer
				value := tc.value
				if nested {
					value = map[string]any{"value": value}
				}
				logx.New(&out, logx.Options{}).Info("event", "credential", value)
				if strings.Contains(out.String(), secret) || !strings.Contains(out.String(), tc.want) {
					t.Fatalf("custom serialization lost (nested=%t): %s", nested, out.String())
				}
			}
		})
	}
}
