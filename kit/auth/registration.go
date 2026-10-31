package auth

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/stopyellingatme/spinup/kit/httpx"
)

// RegistrationMode controls self-service account creation.
type RegistrationMode string

const (
	// RegistrationOpen allows accounts with the default role.
	RegistrationOpen RegistrationMode = "open"
	// RegistrationFirstUser allows only the first account, with the administrator role.
	RegistrationFirstUser RegistrationMode = "first-user"
	// RegistrationClosed prevents self-service account creation.
	RegistrationClosed RegistrationMode = "closed"
)

// ParseRegistration parses AUTH_REGISTRATION. An empty value means open registration.
func ParseRegistration(s string) (RegistrationMode, error) {
	switch RegistrationMode(s) {
	case "", RegistrationOpen:
		return RegistrationOpen, nil
	case RegistrationFirstUser:
		return RegistrationFirstUser, nil
	case RegistrationClosed:
		return RegistrationClosed, nil
	default:
		const hint = "\nTry: set AUTH_REGISTRATION to one of these values."
		return "", fmt.Errorf("AUTH_REGISTRATION: invalid value %q. Valid values: open, first-user, closed.%s", s, hint)
	}
}

// WithRegistration sets the mode for self-service and OIDC account creation.
// New validates the mode. The default is RegistrationOpen.
func WithRegistration(m RegistrationMode) Option {
	return func(o *options) { o.registration = m }
}

var errRegistrationClosed = &userError{message: "Registration is closed. Try: ask an administrator to create your account."}

func registrationClosed(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	httpx.WriteProblem(w, r, httpx.Problem{Status: http.StatusForbidden, Detail: errRegistrationClosed.Error()})
}

// firstUser locks account changes before counting rows in the transaction.
// ReadCommitted ensures the count sees the previous lock holder's insert.
func firstUser(ctx context.Context, tx pgx.Tx) (bool, error) {
	if err := lockUsers(ctx, tx); err != nil {
		return false, err
	}
	var count int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		return false, err
	}
	return count == 0, nil
}

// registrationRole checks the mode and returns the role for a new account.
// Its transaction retains the first-user lock until commit or rollback.
func (a *Auth) registrationRole(ctx context.Context, tx pgx.Tx) (string, error) {
	switch a.registration {
	case RegistrationClosed:
		return "", errRegistrationClosed
	case RegistrationFirstUser:
		first, err := firstUser(ctx, tx)
		if err != nil {
			return "", err
		}
		if !first {
			return "", errRegistrationClosed
		}
		return a.roles.Admin, nil
	default:
		return a.roles.Default, nil
	}
}
