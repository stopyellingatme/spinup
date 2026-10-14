package ui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestExitCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, ExitOK},
		{"plain", errors.New("x"), ExitError},
		{"zero code", &Error{What: "x"}, ExitError},
		{"usage", &Error{What: "x", Code: ExitUsage}, ExitUsage},
		{"wrapped", fmt.Errorf("w: %w", &Error{What: "x", Code: ExitDrift}), ExitDrift},
		{"locked", &Error{What: "x", Code: ExitLocked}, ExitLocked},
		{"conflicts", &Error{What: "x", Code: ExitConflicts}, ExitConflicts},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ExitCode(tc.err); got != tc.want {
				t.Fatalf("ExitCode = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestErrorStringAndUnwrap(t *testing.T) {
	t.Parallel()
	cause := errors.New("cause")
	e := &Error{What: "a", Why: "b", Err: cause}
	if e.Error() != "a: b" {
		t.Fatalf("got %q", e.Error())
	}
	if (&Error{What: "a"}).Error() != "a" {
		t.Fatal("What only")
	}
	if !errors.Is(e, cause) {
		t.Fatal("Unwrap failed")
	}
}

func TestRenderError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		opts Options
		err  error
		want string
	}{
		{"full", Options{}, &Error{What: "Cannot read file", Why: "it is missing", Try: "spinup doctor"},
			"Error: Cannot read file\n  it is missing\n  Try: spinup doctor\n"},
		{"what only", Options{}, &Error{What: "Failed"}, "Error: Failed\n"},
		{"plain", Options{}, errors.New("boom"), "Error: boom\n"},
		{"json", Options{JSON: true}, &Error{What: "w", Why: "y", Try: "t", Code: 3},
			`{"error":{"what":"w","why":"y","try":"t","code":3}}` + "\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out, errOut bytes.Buffer
			New(nil, &out, &errOut, tc.opts).RenderError(tc.err)
			if errOut.String() != tc.want {
				t.Fatalf("got %q, want %q", errOut.String(), tc.want)
			}
			if out.Len() != 0 {
				t.Fatalf("stdout not empty: %q", out.String())
			}
		})
	}
}

func TestJSONAndMessages(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	p := New(nil, &out, &errOut, Options{})
	if err := p.JSON(map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out.Bytes()) || !strings.HasSuffix(out.String(), "\n") || !strings.Contains(out.String(), "  \"a\"") {
		t.Fatalf("bad JSON output %q", out.String())
	}
	out.Reset()
	p.Println("x", "y")
	p.Printf("%d\n", 2)
	p.Infof("info %s", "z")
	p.Successf("ok")
	if out.String() != "x y\n2\ninfo z\n✓ ok\n" {
		t.Fatalf("got %q", out.String())
	}
	p.Warnf("careful")
	if errOut.String() != "Warning: careful\n" {
		t.Fatalf("got %q", errOut.String())
	}
}

func TestJSONModeSilencesStatus(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	p := New(nil, &out, &errOut, Options{JSON: true})
	p.Infof("a")
	p.Successf("b")
	if out.Len() != 0 {
		t.Fatalf("got %q", out.String())
	}
}

func TestInteractiveAndColorOnBuffers(t *testing.T) {
	t.Parallel()
	p := New(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}, Options{})
	if p.Interactive() {
		t.Fatal("buffers must not be interactive")
	}
	if p.Color() {
		t.Fatal("buffers must not have color")
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("CI", "1")
	t.Setenv("NO_COLOR", "1")
	p := New(nil, &bytes.Buffer{}, &bytes.Buffer{}, Options{})
	if p.Interactive() || p.Color() {
		t.Fatal("env must disable both")
	}
}
