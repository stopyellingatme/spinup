package apikeys

import (
	"context"
	"time"
)

// Key identifies a stored API key. Its token is never returned in a list.
type Key struct {
	// ID is the key ID.
	ID int64 `json:"id"`
	// UserID is the ID of the user who owns the key.
	UserID int64 `json:"-"`
	// Name is the label selected by the user.
	Name string `json:"name"`
	// Scope selects the permitted request methods.
	Scope Scope `json:"scope"`
	// Display identifies the token without revealing its secret.
	Display string `json:"display"`
	// CreatedAt is when the key was created.
	CreatedAt time.Time `json:"created_at"`
	// LastUsedAt is the time of the most recent usage flush.
	LastUsedAt *time.Time `json:"last_used_at"`
	// ExpiresAt is the expiry time, or nil for no expiry.
	ExpiresAt *time.Time `json:"expires_at"`
	// RevokedAt is the revocation time, or nil for an active key.
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// Request is what a key request carries in its context.
type Request struct {
	// Key identifies the authenticated key.
	Key Key
	// PlanName is the effective declared plan name.
	PlanName string
	// Plan holds the effective quotas.
	Plan Plan
}

type requestKey struct{}

// KeyFrom returns the API key and its plan from ctx.
func KeyFrom(ctx context.Context) (Request, bool) {
	r, ok := ctx.Value(requestKey{}).(Request)
	return r, ok
}

// CreateInput describes a new key. An ExpiresIn of zero means no expiry.
type CreateInput struct {
	// Name is the label for the new key.
	Name string
	// Scope selects read or write access.
	Scope Scope
	// ExpiresIn is zero or a duration of 1 to 365 whole days.
	ExpiresIn time.Duration
}

// UsageDay is the stored request count for one key and UTC day.
type UsageDay struct {
	// KeyID identifies the key that made these requests.
	KeyID int64 `json:"key_id"`
	// Day is the UTC date in YYYY-MM-DD format.
	Day string `json:"day"`
	// Count is the number of accepted requests.
	Count int64 `json:"count"`
}

// Account describes a user with keys or an explicit plan.
type Account struct {
	// UserID identifies the account.
	UserID int64 `json:"user_id"`
	// Email is the normalized email address.
	Email string `json:"email"`
	// Role is the stored role of the user.
	Role string `json:"role"`
	// Plan is the effective declared plan name.
	Plan string `json:"plan"`
	// Today is the stored request total for the current UTC day.
	Today int64 `json:"today"`
	// Keys contains the keys that are not revoked.
	Keys []Key `json:"keys"`
}
