package auth

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

var errResetNotFound = pgx.ErrNoRows

// reserveResetRequest applies the email limit before reserving global capacity.
// Repeated requests for one email cannot exhaust the global bucket.
func (a *Auth) reserveResetRequest(ctx context.Context, email string, now time.Time) (bool, error) {
	until, _, _, err := a.reserveAttempt(ctx, "reset-email:"+email, now)
	if err != nil || !until.IsZero() {
		return false, err
	}
	// Reuse the database reservation with a separate threshold and the same window.
	// Construct only the limiter fields. Do not copy Auth's synchronization state.
	global := Auth{pool: a.pool, threshold: resetGlobalThreshold, window: a.window}
	until, _, _, err = global.reserveAttempt(ctx, "reset-global", now)
	return err == nil && until.IsZero(), err
}

// consumeReset updates the password, consumes all reset tokens, increments the
// auth version (which ends the sessions of the user), and clears lockout in one
// transaction. A rollback preserves the token.
func (a *Auth) consumeReset(ctx context.Context, digest []byte, hash string) (int64, error) {
	tx, err := a.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// Lock the user before the token. Different tokens of one user serialize here.
	var user User
	if err := tx.QueryRow(ctx, `SELECT u.id,u.email,u.role FROM users u JOIN password_resets p ON p.user_id = u.id WHERE p.token_hash = $1 FOR UPDATE OF u`, digest).Scan(&user.ID, &user.Email, &user.Role); err != nil {
		return 0, err
	}
	id, email := user.ID, user.Email
	user.Role = a.effectiveRole(user.Role)
	var expires time.Time
	var used *time.Time
	if err := tx.QueryRow(ctx, `SELECT expires_at,used_at FROM password_resets WHERE token_hash = $1 FOR UPDATE`, digest).Scan(&expires, &used); err != nil {
		return 0, err
	}
	now := a.now()
	if used != nil || !now.Before(expires) {
		return 0, errResetNotFound
	}
	if err := a.rowEvent(ctx, tx, Event{Name: EventPasswordReset, User: user}); err != nil {
		return 0, err
	}
	// The new auth version ends every session of the user, also a login that
	// read the old version and commits its session after this transaction.
	// No other session row is read.
	if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2,updated_at = $3,auth_version = auth_version + 1 WHERE id = $1`, id, hash, now); err != nil {
		return 0, schemaHint(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE password_resets SET used_at = $2 WHERE user_id = $1 AND used_at IS NULL`, id, now); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM login_attempts WHERE email = $1`, email); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}
