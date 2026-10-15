package auth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// oidcUser resolves an identity before considering its email. A provider cannot
// move an identity to another account by changing the email claim.
func (a *Auth) oidcUser(ctx context.Context, provider, subject, email string, verified bool) (User, error) {
	if !verified {
		return User{}, errors.New("auth: cannot link an unverified OIDC email\nTry: verify the email with the provider")
	}
	tx, err := a.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// Serialize callbacks of this identity, including callbacks with different emails.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, provider+":"+subject); err != nil {
		return User{}, err
	}
	var user User
	err = tx.QueryRow(ctx, `SELECT u.id,u.email FROM users u JOIN user_identities i ON i.user_id = u.id WHERE i.provider = $1 AND i.subject = $2`, provider, subject).Scan(&user.ID, &user.Email)
	if err == nil {
		return user, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return User{}, err
	}
	// The unique email key serializes different identities that share an email.
	// Lock an existing account before removing its password and sessions.
	err = tx.QueryRow(ctx, `INSERT INTO users (email,password_hash) VALUES ($1,NULL) ON CONFLICT (email) DO NOTHING RETURNING id,email`, email).Scan(&user.ID, &user.Email)
	existing := errors.Is(err, pgx.ErrNoRows)
	if existing {
		err = tx.QueryRow(ctx, `SELECT id,email FROM users WHERE email = $1 FOR UPDATE`, email).Scan(&user.ID, &user.Email)
	}
	if err != nil {
		return User{}, err
	}
	if existing {
		if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = NULL WHERE id = $1`, user.ID); err != nil {
			return User{}, err
		}
		if err := a.deleteOIDCSessions(ctx, tx, user.ID); err != nil {
			return User{}, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_identities (provider,subject,user_id,email) VALUES ($1,$2,$3,$4)`, provider, subject, user.ID, email); err != nil {
		return User{}, err
	}
	return user, tx.Commit(ctx)
}

// deleteOIDCSessions removes the user's scs sessions within the linking transaction.
func (a *Auth) deleteOIDCSessions(ctx context.Context, tx pgx.Tx, id int64) error {
	rows, err := tx.Query(ctx, `SELECT token,data FROM sessions`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var tokens []string
	for rows.Next() {
		var token string
		var data []byte
		if err := rows.Scan(&token, &data); err != nil {
			return err
		}
		// Decode with the configured scs codec. Stored tokens are already hashed.
		_, values, err := a.sessions.Codec.Decode(data)
		if err != nil {
			return err
		}
		if userID, ok := values[sessionUserKey].(int64); ok && userID == id {
			tokens = append(tokens, token)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM sessions WHERE token = ANY($1::text[])`, tokens)
	return err
}
