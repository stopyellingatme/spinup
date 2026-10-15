package jobs

// userError is an error for a person to read. It says what failed and why,
// and its text ends with a "Try:" line.
type userError struct {
	what string
	why  string
	try  string
	err  error
}

// Error returns "what: why", then a "Try:" line.
func (e *userError) Error() string {
	s := e.what
	if e.why != "" {
		s += ": " + e.why
	}
	if e.try != "" {
		s += "\nTry: " + e.try
	}
	return s
}

// Unwrap returns the cause.
func (e *userError) Unwrap() error { return e.err }
