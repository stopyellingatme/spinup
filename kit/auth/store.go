package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stopyellingatme/spinup/kit/db"
)

// userColumns qualifies columns only for queries without joins.
const userColumns = "id, email, role"

// userColumnsU avoids ambiguous email columns in identity joins.
const userColumnsU = "u.id, u.email, u.role"

// effectiveRole maps a stored role without granting implicit administrator access.
func (a *Auth) effectiveRole(role string) string {
	if role == "" && a.roles.Default != a.roles.Admin {
		return a.roles.Default
	}
	return role
}

// userByID returns the user with id. found is false when no such user exists.
func (a *Auth) userByID(ctx context.Context, id int64) (user User, found bool, err error) {
	err = a.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id).Scan(&user.ID, &user.Email, &user.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, false, nil
	}
	user.Role = a.effectiveRole(user.Role)
	return user, err == nil, err
}

// credentialsByEmail returns the user and the password hash for email. An account without a
// password (OIDC only) is not found, so the password login gives the same generic error.
func (a *Auth) credentialsByEmail(ctx context.Context, email string) (user User, hash string, found bool, err error) {
	err = a.pool.QueryRow(ctx, `SELECT `+userColumns+`, password_hash FROM users WHERE email = $1 AND password_hash IS NOT NULL`, email).Scan(&user.ID, &user.Email, &user.Role, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", false, nil
	}
	user.Role = a.effectiveRole(user.Role)
	return user, hash, err == nil, err
}

// createUser applies the registration mode and inserts a user.
// created is false when the email is taken.
func (a *Auth) createUser(ctx context.Context, email, hash string) (user User, created bool, err error) {
	tx, err := a.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return User{}, false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	role, err := a.registrationRole(ctx, tx)
	if err != nil {
		return User{}, false, err
	}
	if err := a.rowEvent(ctx, tx, Event{Name: EventRegister, User: User{Email: email, Role: role}}); err != nil {
		return User{}, false, err
	}
	err = tx.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, role) VALUES ($1, $2, $3) ON CONFLICT (email) DO NOTHING RETURNING id, email, role`,
		email, hash, role).Scan(&user.ID, &user.Email, &user.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, false, err
	}
	user.Role = a.effectiveRole(user.Role)
	return user, true, nil
}

// updatePasswordHash replaces the hash of user, but only when it still equals old.
// It runs in a transaction with the password_rehash event.
func (a *Auth) updatePasswordHash(ctx context.Context, user User, old, hash string) error {
	return db.InTx(ctx, a.pool, func(tx pgx.Tx) error {
		if err := a.rowEvent(ctx, tx, Event{Name: EventPasswordRehash, User: user}); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE users SET password_hash = $3, updated_at = now() WHERE id = $1 AND password_hash = $2`, user.ID, old, hash)
		return err
	})
}

// reserveAttempt checks the lockout and records an attempt before password verification.
// It returns the ID of the recorded attempt, so releaseAttempt can take it back.
// The email lock serializes reservations across all instances that share the database.
// A nonzero time means the threshold was reached, so no attempt was recorded.
// last is true when the recorded attempt is the one that reaches the threshold:
// if it fails, the lockout starts.
func (a *Auth) reserveAttempt(ctx context.Context, email string, now time.Time) (until time.Time, id int64, last bool, err error) {
	// Each statement must see reservations committed by the previous lock holder.
	tx, err := a.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return time.Time{}, 0, false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, email); err != nil {
		return time.Time{}, 0, false, err
	}
	rows, err := tx.Query(ctx,
		`SELECT attempted_at FROM login_attempts WHERE email = $1 AND attempted_at > $2 ORDER BY attempted_at DESC LIMIT $3`,
		email, now.Add(-a.window), a.threshold)
	if err != nil {
		return time.Time{}, 0, false, err
	}
	times, err := pgx.CollectRows(rows, pgx.RowTo[time.Time])
	if err != nil {
		return time.Time{}, 0, false, err
	}
	if len(times) >= a.threshold {
		return times[len(times)-1].Add(a.window), 0, false, nil
	}
	if err := tx.QueryRow(ctx, `INSERT INTO login_attempts (email, attempted_at) VALUES ($1, $2) RETURNING id`, email, now).Scan(&id); err != nil {
		return time.Time{}, 0, false, err
	}
	return time.Time{}, id, len(times)+1 == a.threshold, tx.Commit(ctx)
}

// releaseAttempt deletes the attempt with id. A login that checked no password
// releases its attempt, so a busy server never counts toward a lockout.
func (a *Auth) releaseAttempt(ctx context.Context, id int64) error {
	_, err := a.pool.Exec(ctx, `DELETE FROM login_attempts WHERE id = $1`, id)
	return err
}

// clearFailures deletes the failed logins of email.
func (a *Auth) clearFailures(ctx context.Context, email string) error {
	_, err := a.pool.Exec(ctx, `DELETE FROM login_attempts WHERE email = $1`, email)
	return err
}

// deleteExpired deletes expired sessions and failed logins outside the window.
func (a *Auth) deleteExpired(ctx context.Context) error {
	if _, err := a.pool.Exec(ctx, `DELETE FROM sessions WHERE expiry < now()`); err != nil {
		return err
	}
	_, err := a.pool.Exec(ctx, `DELETE FROM login_attempts WHERE attempted_at < $1`, a.now().Add(-a.window))
	return err
}
