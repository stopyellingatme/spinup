package ui

import "errors"

// Exit codes returned by the spinup process.
const (
	// ExitOK means success.
	ExitOK = 0
	// ExitError means a general failure.
	ExitError = 1
	// ExitUsage means a bad command, flag, or argument count.
	ExitUsage = 2
	// ExitConflicts means an operation stopped with merge conflicts.
	ExitConflicts = 3
	// ExitDrift means the project differs from the expected state.
	ExitDrift = 4
	// ExitLocked means another spinup process holds the lock.
	ExitLocked = 5
)

// Error is a user-facing error. It says what failed, why, and what to try.
type Error struct {
	// What states what failed.
	What string
	// Why states the cause. It is optional.
	Why string
	// Try states the next step for the user. It is optional.
	Try string
	// Code is the process exit code. Zero means ExitError.
	Code int
	// Err is the wrapped cause. It is optional.
	Err error
}

// Error returns What, followed by ": Why" when Why is set.
func (e *Error) Error() string {
	if e.Why != "" {
		return e.What + ": " + e.Why
	}
	return e.What
}

// Unwrap returns the wrapped cause.
func (e *Error) Unwrap() error { return e.Err }

// ExitCode maps an error to a process exit code. A nil error gives ExitOK.
// A wrapped *Error gives its Code, or ExitError when the Code is zero.
// Any other error gives ExitError.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var ue *Error
	if errors.As(err, &ue) && ue.Code != 0 {
		return ue.Code
	}
	return ExitError
}
