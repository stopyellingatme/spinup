package authsink

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stopyellingatme/spinup/kit/audit"
	"github.com/stopyellingatme/spinup/kit/auth"
	"github.com/stopyellingatme/spinup/kit/httpx"
)

// actionPrefix starts the action of an explicit auth event, such as auth.sign_in.
const actionPrefix = "auth."

// New returns the auth.EventSink of the audit trail (ADR 0025 decision 4.6).
//
// For a row change of users (register, user_create, role_change,
// password_reset, password_rehash), the sink binds the actor to the
// transaction q with audit.Bind. The trigger on users then writes the event.
// For sign_in, sign_out, and lockout, it writes one event auth.<name> on the
// table users with audit.Record. When q is nil, it uses pool.
func New(pool *pgxpool.Pool) auth.EventSink {
	return func(ctx context.Context, q auth.Execer, e auth.Event) error {
		var x audit.Execer = q
		if q == nil {
			if pool == nil {
				return errors.New("authsink: no database pool or transaction\nTry: pass the pool from db.Open to authsink.New")
			}
			x = pool
		}
		switch e.Name {
		case auth.EventRegister, auth.EventUserCreate, auth.EventRoleChange, auth.EventPasswordReset, auth.EventPasswordRehash:
			return audit.Bind(audit.WithActor(ctx, rowActor(ctx, e)), x)
		case auth.EventSignIn, auth.EventSignOut, auth.EventLockout:
			return audit.Record(ctx, x, recordEvent(ctx, e))
		default:
			return fmt.Errorf("authsink: unknown auth event %q\nTry: update kit/audit/authsink together with kit/auth", e.Name)
		}
	}
}

// ActorContext returns ctx with the signed-in user u as the audit actor: the kind
// user, the source api, and the request ID of ctx. Pass it to
// auth.WithActorContext, so handlers reach audit.InTx with this actor.
func ActorContext(ctx context.Context, u auth.User) context.Context {
	return audit.WithActor(ctx, userActor(ctx, u, ""))
}

func userActor(ctx context.Context, u auth.User, reason string) audit.Actor {
	return audit.Actor{Kind: audit.KindUser, ID: u.ID, Email: u.Email, Role: u.Role, Source: audit.SourceAPI,
		Reason: reason, RequestID: httpx.RequestIDFrom(ctx)}
}

// rowActor returns the actor of a row change of users.
func rowActor(ctx context.Context, e auth.Event) audit.Actor {
	reason := e.Reason
	switch e.Name {
	case auth.EventRegister:
		// The new account does not have an ID before the insert. An OIDC sign-in
		// that links an existing account has one.
		if reason == "" {
			reason = e.Name
			if e.Method != "" {
				reason += ":" + e.Method
			}
		}
		if e.User.ID != 0 {
			return userActor(ctx, e.User, reason)
		}
		return audit.Actor{Kind: audit.KindSystem, Email: e.User.Email, Role: e.User.Role, Source: audit.SourceAPI,
			Reason: reason, RequestID: httpx.RequestIDFrom(ctx)}
	case auth.EventPasswordReset, auth.EventPasswordRehash:
		// The subject account changes its own password.
		if reason == "" {
			reason = e.Name
		}
		return userActor(ctx, e.User, reason)
	}
	// user_create and role_change.
	if e.Actor != "" {
		return audit.Actor{Kind: audit.KindOperator, Email: e.Actor, Source: audit.SourceCLI, Reason: reason}
	}
	if actor, ok := audit.ActorFrom(ctx); ok {
		if reason != "" {
			actor.Reason = reason
		}
		return actor
	}
	if reason == "" {
		reason = e.Name
	}
	return audit.Actor{Kind: audit.KindSystem, Reason: reason, RequestID: httpx.RequestIDFrom(ctx)}
}

// recordEvent returns the explicit event of a sign-in, a sign-out, or a lockout.
func recordEvent(ctx context.Context, e auth.Event) audit.Event {
	ev := audit.Event{Action: actionPrefix + e.Name, Table: "users", RecordID: strconv.FormatInt(e.User.ID, 10)}
	if e.Method != "" {
		ev.Changes, _ = json.Marshal(map[string]string{"method": e.Method})
	}
	if e.Name == auth.EventLockout {
		// The account owner did not act: the failed logins come from anybody.
		// The subject is the record ID. The actor is the app.
		ev.Actor = audit.Actor{Kind: audit.KindSystem, Source: audit.SourceAPI, Reason: e.Name, RequestID: httpx.RequestIDFrom(ctx)}
		return ev
	}
	ev.Actor = userActor(ctx, e.User, e.Reason)
	return ev
}
