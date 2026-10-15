package ui

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestRenderErrorShowsOneTry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"plain nested", fmt.Errorf("outer: %w\nTry: outer hint", errors.New("inner\nTry: inner hint")), "Error: outer: inner\n  Try: outer hint\n"},
		{"ui error wins", &Error{What: "Upgrade failed", Why: "step: x\nTry: low\nTry: mid", Try: "top"}, "Error: Upgrade failed\n  step: x\n  Try: top\n"},
		{"ui error without try", &Error{What: "Failed", Why: "x\nTry: only"}, "Error: Failed\n  x\n  Try: only\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out, errOut bytes.Buffer
			New(nil, &out, &errOut, Options{NoColor: true}).RenderError(tc.err)
			if errOut.String() != tc.want {
				t.Fatalf("got %q, want %q", errOut.String(), tc.want)
			}
		})
	}
}
