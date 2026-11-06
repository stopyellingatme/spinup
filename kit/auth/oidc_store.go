package auth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// oidcUser resolves an identity before considering its email. A provider cannot
// move an identity to another account by changing the email claim. It also
// returns the auth version that the new session stores.
func (a *Auth) oidcUser(ctx context.Context, provider, subject, email string, verified bool) (User, int64, error) {
	if !verified {
		return User{}, 0, errors.New("auth: cannot link an unverified OIDC email\nTry: verify the email with the provider")
	}
	tx, err := a.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return User{}, 0, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// Serialize callbacks of this identity, including callbacks with different emails.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, provider+":"+subject); err != nil {
		return User{}, 0, err
	}
	var user User
	var version int64
	err = tx.QueryRow(ctx, `SELECT `+userColumnsU+`, u.auth_version FROM users u JOIN user_identities i ON i.user_id = u.id WHERE i.provider = $1 AND i.subject = $2`, provider, subject).Scan(&user.ID, &user.Email, &user.Role, &version)
	if err == nil {
		user.Role = a.effectiveRole(user.Role)
		return user, version, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return User{}, 0, schemaHint(err)
	}
	// The unique email key serializes different identities that share an email.
	// Lock an existing account before removing its password and sessions.
	err = tx.QueryRow(ctx, `SELECT id, email, role, auth_version FROM users WHERE email = $1 FOR UPDATE`, email).Scan(&user.ID, &user.Email, &user.Role, &version)
	existing := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		// Linking a verified email to an existing account remains available.
		// Only creation applies the registration mode and the first-user lock.
		role, roleErr := a.registrationRole(ctx, tx)
		if roleErr != nil {
			return User{}, 0, roleErr
		}
		if err := a.rowEvent(ctx, tx, Event{Name: EventRegister, User: User{Email: email, Role: role}, Method: "oidc"}); err != nil {
			return User{}, 0, err
		}
		err = tx.QueryRow(ctx, `INSERT INTO users (email, password_hash, role) VALUES ($1, NULL, $2) ON CONFLICT (email) DO NOTHING RETURNING id, email, role, auth_version`, email, role).Scan(&user.ID, &user.Email, &user.Role, &version)
		existing = errors.Is(err, pgx.ErrNoRows)
		if existing {
			err = tx.QueryRow(ctx, `SELECT id, email, role, auth_version FROM users WHERE email = $1 FOR UPDATE`, email).Scan(&user.ID, &user.Email, &user.Role, &version)
		}
	}
	if err != nil {
		return User{}, 0, schemaHint(err)
	}
	if existing {
		// The existing account links the identity. It acts on its own account.
		if err := a.rowEvent(ctx, tx, Event{Name: EventRegister, User: User{ID: user.ID, Email: user.Email, Role: a.effectiveRole(user.Role)}, Method: "oidc"}); err != nil {
			return User{}, 0, err
		}
		// The new auth version ends every earlier session of the account, also a
		// password login that commits its session after this transaction. No
		// other session row is read.
		if err := tx.QueryRow(ctx, `UPDATE users SET password_hash = NULL, auth_version = auth_version + 1 WHERE id = $1 RETURNING auth_version`, user.ID).Scan(&version); err != nil {
			return User{}, 0, err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_identities (provider,subject,user_id,email) VALUES ($1,$2,$3,$4)`, provider, subject, user.ID, email); err != nil {
		return User{}, 0, err
	}
	user.Role = a.effectiveRole(user.Role)
	return user, version, tx.Commit(ctx)
}
