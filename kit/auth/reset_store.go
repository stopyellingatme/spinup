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
	until, _, err := a.reserveAttempt(ctx, "reset-email:"+email, now)
	if err != nil || !until.IsZero() {
		return false, err
	}
	// Reuse the database reservation with a separate threshold and the same window.
	// Construct only the limiter fields. Do not copy Auth's synchronization state.
	global := Auth{pool: a.pool, threshold: resetGlobalThreshold, window: a.window}
	until, _, err = global.reserveAttempt(ctx, "reset-global", now)
	return err == nil && until.IsZero(), err
}

// consumeReset updates the password, consumes all reset tokens, removes sessions,
// and clears lockout in one transaction. A rollback preserves the token.
func (a *Auth) consumeReset(ctx context.Context, digest []byte, hash string) (int64, error) {
	tx, err := a.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// Lock the user before the token. Different tokens of one user serialize here.
	var id int64
	var email string
	if err := tx.QueryRow(ctx, `SELECT u.id,u.email FROM users u JOIN password_resets p ON p.user_id = u.id WHERE p.token_hash = $1 FOR UPDATE OF u`, digest).Scan(&id, &email); err != nil {
		return 0, err
	}
	var expires time.Time
	var used *time.Time
	if err := tx.QueryRow(ctx, `SELECT expires_at,used_at FROM password_resets WHERE token_hash = $1 FOR UPDATE`, digest).Scan(&expires, &used); err != nil {
		return 0, err
	}
	now := a.now()
	if used != nil || !now.Before(expires) {
		return 0, errResetNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2,updated_at = $3 WHERE id = $1`, id, hash, now); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE password_resets SET used_at = $2 WHERE user_id = $1 AND used_at IS NULL`, id, now); err != nil {
		return 0, err
	}
	// scs encodes auth.user_id in the session data. Decode with its configured codec,
	// and delete the stored token directly. HashTokenInStore is already applied.
	rows, err := tx.Query(ctx, `SELECT token,data FROM sessions`)
	if err != nil {
		return 0, err
	}
	var tokens []string
	for rows.Next() {
		var token string
		var data []byte
		if err := rows.Scan(&token, &data); err != nil {
			rows.Close()
			return 0, err
		}
		_, values, err := a.sessions.Codec.Decode(data)
		if err != nil {
			rows.Close()
			return 0, err
		}
		if userID, ok := values[sessionUserKey].(int64); ok && userID == id {
			tokens = append(tokens, token)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE token = ANY($1::text[])`, tokens); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM login_attempts WHERE email = $1`, email); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}
