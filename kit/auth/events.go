package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/stopyellingatme/spinup/kit/httpx"
)

// The names of the auth events (ADR 0025 decision 4.6).
const (
	// EventRegister is a new account through the register endpoint, or through
	// an OIDC sign-in (Event.Method "oidc"). An OIDC sign-in that links a new
	// identity to an existing account is also EventRegister, with Event.User.ID set.
	EventRegister = "register"
	// EventUserCreate is a new account through the operator command.
	EventUserCreate = "user_create"
	// EventRoleChange is a change of the role of an account.
	EventRoleChange = "role_change"
	// EventPasswordReset is a new password through a reset token.
	EventPasswordReset = "password_reset"
	// EventPasswordRehash is a new hash of the same password with new parameters.
	EventPasswordRehash = "password_rehash"
	// EventSignIn is a successful sign-in. Event.Method is "password" or "oidc".
	EventSignIn = "sign_in"
	// EventSignOut is a sign-out.
	EventSignOut = "sign_out"
	// EventLockout is the start of a lockout of an existing account.
	EventLockout = "lockout"
)

// Event is an account event that auth gives to the EventSink.
type Event struct {
	// Name is one of the Event* names, such as EventSignIn.
	Name string
	// User is the subject account.
	User User
	// Actor is the email of the operator for the operator command, or empty.
	Actor string
	// Method is the sign-in method, "password" or "oidc", or empty.
	Method string
	// Reason is free text, such as the --reason flag of the operator command.
	Reason string
}

// Execer runs a statement. A *pgxpool.Pool, a *pgx.Conn, and a pgx.Tx satisfy it.
// It has the same method set as audit.Execer, so a sink can pass it on without a cast.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// EventSink receives the account events. For a row change of users (register,
// user_create, role_change, password_reset, password_rehash), auth calls it inside
// the transaction of the change with that transaction as q, and an error rolls the
// change back. For sign_in, sign_out, and lockout, auth calls it after the effect
// with the pool as q, and logs an error without a stop.
type EventSink func(ctx context.Context, q Execer, e Event) error

// WithEvents sets the sink of the account events. Without it, auth records no
// explicit event. The audit triggers still record the row changes.
func WithEvents(s EventSink) Option { return func(o *options) { o.events = s } }

// WithActorContext sets a function that the middleware calls for each request
// with a signed-in user. It returns the context of the request, such as a context
// with an audit actor (authsink.ActorContext).
func WithActorContext(f func(ctx context.Context, u User) context.Context) Option {
	return func(o *options) { o.actorContext = f }
}

// operatorKey is the context key of the operator of the user command.
type operatorKey struct{}

// operator is the operator of the user command and the --reason flag.
type operator struct {
	email, reason string
}

// withOperator returns ctx with the operator of the user command, so CreateUser,
// SetRole, and BackfillRoles put it into Event.Actor and Event.Reason.
func withOperator(ctx context.Context, email, reason string) context.Context {
	return context.WithValue(ctx, operatorKey{}, operator{email: email, reason: reason})
}

// operatorEmail returns the actor email of the user command (ADR 0025 decision
// 4.7): operator:<login>, where the login is SUDO_USER, then USER, then unknown.
func operatorEmail(getenv func(string) string) string {
	login := getenv("SUDO_USER")
	if login == "" {
		login = getenv("USER")
	}
	if login == "" {
		login = "unknown"
	}
	return "operator:" + login
}

// withOperatorOf sets Event.Actor and Event.Reason from the operator of ctx.
func withOperatorOf(ctx context.Context, e Event) Event {
	if op, ok := ctx.Value(operatorKey{}).(operator); ok {
		e.Actor = op.email
		if op.reason != "" {
			e.Reason = op.reason
		}
	}
	return e
}

// rowEvent gives the event of a row change of users to the sink, inside the
// transaction tx of the change and before the change, so the audit trigger
// sees the actor. An error of the sink must roll the change back.
func (a *Auth) rowEvent(ctx context.Context, tx Execer, e Event) error {
	if a.events == nil {
		return nil
	}
	err := a.events(ctx, tx, e)
	switch {
	case err == nil:
		return nil
	case strings.Contains(err.Error(), "\nTry:"):
		return fmt.Errorf("auth: record the %s event: %w", e.Name, err)
	default:
		return fmt.Errorf("auth: record the %s event: %w\nTry: check the event sink of auth.WithEvents and that the audit migration ran (spinup db migrate up)", e.Name, err)
	}
}

// notify gives a sign-in, sign-out, or lockout event to the sink after the
// effect. A failure is logged and does not stop the request (ADR 0025 decision 1.4).
func (a *Auth) notify(ctx context.Context, e Event) {
	if a.events == nil {
		return
	}
	if err := a.events(ctx, a.pool, e); err != nil {
		a.logger.WarnContext(ctx, "auth: cannot record an account event", "event", e.Name, "user_id", e.User.ID,
			"error", err, "request_id", httpx.RequestIDFrom(ctx))
	}
}
