package auth

import (
	"context"

	"github.com/jackc/pgx/v5/pgconn"
)

// The names of the auth events (ADR 0025 decision 4.6).
const (
	// EventRegister is a new account through the register endpoint.
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
