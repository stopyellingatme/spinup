// Package authsink connects kit/auth to kit/audit (ADR 0025 decisions 4.4 and
// 4.5), so neither package imports the other.
//
// New returns the auth.EventSink that binds the actor for a row change of users
// and records sign-in, sign-out, and lockout events. ActorContext puts the
// signed-in user into the context as an audit actor. Wire both in main.go:
//
//	authn, err := auth.New(pool,
//		auth.WithEvents(authsink.New(pool)),
//		auth.WithActorContext(authsink.ActorContext),
//	)
package authsink
