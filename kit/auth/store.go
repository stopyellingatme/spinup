package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// userByID returns the user with id. found is false when no such user exists.
func (a *Auth) userByID(ctx context.Context, id int64) (user User, found bool, err error) {
	err = a.pool.QueryRow(ctx, `SELECT id, email FROM users WHERE id = $1`, id).Scan(&user.ID, &user.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, false, nil
	}
	return user, err == nil, err
}

// credentialsByEmail returns the user and the password hash for email. An account without a
// password (OIDC only) is not found, so the password login gives the same generic error.
func (a *Auth) credentialsByEmail(ctx context.Context, email string) (user User, hash string, found bool, err error) {
	err = a.pool.QueryRow(ctx, `SELECT id, email, password_hash FROM users WHERE email = $1 AND password_hash IS NOT NULL`, email).Scan(&user.ID, &user.Email, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", false, nil
	}
	return user, hash, err == nil, err
}

// createUser inserts a user. created is false when the email is taken.
func (a *Auth) createUser(ctx context.Context, email, hash string) (user User, created bool, err error) {
	err = a.pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash) VALUES ($1, $2) ON CONFLICT (email) DO NOTHING RETURNING id, email`,
		email, hash).Scan(&user.ID, &user.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, false, nil
	}
	return user, err == nil, err
}

// updatePasswordHash replaces the hash of a user, but only when it still equals old.
func (a *Auth) updatePasswordHash(ctx context.Context, id int64, old, hash string) error {
	_, err := a.pool.Exec(ctx, `UPDATE users SET password_hash = $3, updated_at = now() WHERE id = $1 AND password_hash = $2`, id, old, hash)
	return err
}

// reserveAttempt checks the lockout and records an attempt before password verification.
// It returns the ID of the recorded attempt, so releaseAttempt can take it back.
// The email lock serializes reservations across all instances that share the database.
// A nonzero time means the threshold was reached, so no attempt was recorded.
func (a *Auth) reserveAttempt(ctx context.Context, email string, now time.Time) (until time.Time, id int64, err error) {
	// Each statement must see reservations committed by the previous lock holder.
	tx, err := a.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return time.Time{}, 0, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, email); err != nil {
		return time.Time{}, 0, err
	}
	rows, err := tx.Query(ctx,
		`SELECT attempted_at FROM login_attempts WHERE email = $1 AND attempted_at > $2 ORDER BY attempted_at DESC LIMIT $3`,
		email, now.Add(-a.window), a.threshold)
	if err != nil {
		return time.Time{}, 0, err
	}
	times, err := pgx.CollectRows(rows, pgx.RowTo[time.Time])
	if err != nil {
		return time.Time{}, 0, err
	}
	if len(times) >= a.threshold {
		return times[len(times)-1].Add(a.window), 0, nil
	}
	if err := tx.QueryRow(ctx, `INSERT INTO login_attempts (email, attempted_at) VALUES ($1, $2) RETURNING id`, email, now).Scan(&id); err != nil {
		return time.Time{}, 0, err
	}
	return time.Time{}, id, tx.Commit(ctx)
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
