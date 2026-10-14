package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// Options sets the behavior of a Printer.
type Options struct {
	// JSON switches errors to JSON and silences status messages.
	JSON bool
	// NoColor turns off ANSI color.
	NoColor bool
	// NonInteractive forbids prompts.
	NonInteractive bool
	// Verbose turns on Debugf output.
	Verbose bool
}

// Printer writes user-facing output.
type Printer struct {
	in     io.Reader
	out    io.Writer
	errOut io.Writer
	opts   Options
}

// New returns a Printer that reads from in and writes to out and errOut.
func New(in io.Reader, out, errOut io.Writer, opts Options) *Printer {
	return &Printer{in: in, out: out, errOut: errOut, opts: opts}
}

// Println writes a line to out.
func (p *Printer) Println(a ...any) { _, _ = fmt.Fprintln(p.out, a...) }

// Printf writes formatted text to out.
func (p *Printer) Printf(format string, a ...any) { _, _ = fmt.Fprintf(p.out, format, a...) }

// Infof writes an informational line to out. It is silent in JSON mode.
func (p *Printer) Infof(format string, a ...any) {
	if p.opts.JSON {
		return
	}
	_, _ = fmt.Fprintln(p.out, fmt.Sprintf(format, a...))
}

// Successf writes a success line to out. It is silent in JSON mode.
func (p *Printer) Successf(format string, a ...any) {
	if p.opts.JSON {
		return
	}
	_, _ = fmt.Fprintln(p.out, p.paint(p.out, "32", "✓ ")+fmt.Sprintf(format, a...))
}

// Warnf writes a warning line to errOut.
func (p *Printer) Warnf(format string, a ...any) {
	_, _ = fmt.Fprintln(p.errOut, p.paint(p.errOut, "33", "Warning: ")+fmt.Sprintf(format, a...))
}

// Debugf writes a line to errOut when Verbose is set.
func (p *Printer) Debugf(format string, a ...any) {
	if !p.opts.Verbose {
		return
	}
	_, _ = fmt.Fprintln(p.errOut, fmt.Sprintf(format, a...))
}

// JSON writes v as indented JSON, plus a newline, to out.
func (p *Printer) JSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = p.out.Write(append(b, '\n'))
	return err
}

type jsonError struct {
	What string `json:"what"`
	Why  string `json:"why"`
	Try  string `json:"try"`
	Code int    `json:"code"`
}

// RenderError writes err to errOut. It does nothing for a nil error.
func (p *Printer) RenderError(err error) {
	if err == nil {
		return
	}
	what, why, try := err.Error(), "", ""
	var ue *Error
	if errors.As(err, &ue) {
		what, why, try = ue.What, ue.Why, ue.Try
	}
	if p.opts.JSON {
		b, merr := json.Marshal(struct {
			Error jsonError `json:"error"`
		}{jsonError{What: what, Why: why, Try: try, Code: ExitCode(err)}})
		if merr == nil {
			_, _ = p.errOut.Write(append(b, '\n'))
		}
		return
	}
	_, _ = fmt.Fprintln(p.errOut, p.paint(p.errOut, "31;1", "Error:")+" "+what)
	if why != "" {
		_, _ = fmt.Fprintln(p.errOut, "  "+why)
	}
	if try != "" {
		_, _ = fmt.Fprintln(p.errOut, "  Try: "+try)
	}
}

// Interactive reports whether prompts are allowed. It is true only when stdin
// and stdout are terminals, NonInteractive is false, and CI is not set.
func (p *Printer) Interactive() bool {
	if p.opts.NonInteractive || os.Getenv("CI") != "" {
		return false
	}
	return isTerminal(p.in) && isTerminal(p.out)
}

// Color reports whether the Printer writes ANSI color to out.
func (p *Printer) Color() bool { return p.colorFor(p.out) }

func (p *Printer) colorFor(w any) bool {
	if p.opts.NoColor {
		return false
	}
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	return isTerminal(w)
}

// paint wraps s in an ANSI code when w is a terminal and color is allowed.
func (p *Printer) paint(w io.Writer, code, s string) string {
	if !p.colorFor(w) {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd())) //nolint:gosec // file descriptors fit in int
}
