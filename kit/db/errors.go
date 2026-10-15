package db

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

// errEmptyURL is the error for a missing database URL.
func errEmptyURL(what string) error {
	return &userError{
		what: what,
		why:  "the database URL is empty",
		try:  "set DATABASE_URL (see .env.example)",
	}
}

// errParseURL is the error for a database URL that does not parse. It never
// includes the URL or the parser text, because both can hold the password.
func errParseURL(what string) error {
	return &userError{
		what: what,
		why:  "the database URL is not valid (it is not shown because it can hold a password)",
		try:  "use the form postgres://user:password@host:5432/dbname?sslmode=disable",
	}
}
