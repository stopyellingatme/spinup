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

// credentialsByEmail returns the user and the password hash for email.
func (a *Auth) credentialsByEmail(ctx context.Context, email string) (user User, hash string, found bool, err error) {
	err = a.pool.QueryRow(ctx, `SELECT id, email, password_hash FROM users WHERE email = $1`, email).Scan(&user.ID, &user.Email, &hash)
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

// lockedUntil returns the time when logins for email open again, or the zero
// time when they are open now. Logins are locked while the window holds
// threshold failed attempts. The oldest of the last threshold attempts opens
// the lock when it leaves the window.
func (a *Auth) lockedUntil(ctx context.Context, email string, now time.Time) (time.Time, error) {
	rows, err := a.pool.Query(ctx,
		`SELECT attempted_at FROM login_attempts WHERE email = $1 AND attempted_at > $2 ORDER BY attempted_at DESC LIMIT $3`,
		email, now.Add(-a.window), a.threshold)
	if err != nil {
		return time.Time{}, err
	}
	times, err := pgx.CollectRows(rows, pgx.RowTo[time.Time])
	if err != nil || len(times) < a.threshold {
		return time.Time{}, err
	}
	return times[len(times)-1].Add(a.window), nil
}

// recordFailure stores one failed login for email.
func (a *Auth) recordFailure(ctx context.Context, email string, now time.Time) error {
	_, err := a.pool.Exec(ctx, `INSERT INTO login_attempts (email, attempted_at) VALUES ($1, $2)`, email, now)
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
