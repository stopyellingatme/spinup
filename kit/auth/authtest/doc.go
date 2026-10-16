// Package authtest helps tests of handlers that need a signed-in user (ADR 0015 §1).
//
// Wrap a handler with [As] to provide a user. The handler can read the user with
// [auth.UserFrom].
//
//	handler := authtest.As(resourceHandler, 42)
package authtest
