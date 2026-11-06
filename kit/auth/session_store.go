package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// sessionVersionKey is the session key that holds the auth version (int64) of
// the user at login. A session without it has version 1.
const sessionVersionKey = "auth.version"

// sessionStore is the scs store of kit auth. It uses the pgxstore schema plus the
// column sessions.user_id (auth migration 00007). FindCtx reads the session and
// its user in one query, so a request with a session needs one round trip.
// scs hashes the tokens before it calls the store (HashTokenInStore).
type sessionStore struct {
	a *Auth
}

// sessionLookup is the result of FindCtx for one request. Middleware puts an
// empty lookup into the request context, FindCtx fills it, and loadUser reads it.
type sessionLookup struct {
	done    bool  // FindCtx found a session row
	userID  int64 // sessions.user_id, or 0 when the column is NULL
	found   bool  // the users row of userID exists
	user    User  // the stored role, before effectiveRole
	version int64 // users.auth_version
}

// lookupFrom returns the lookup that Middleware put into ctx, or nil.
func lookupFrom(ctx context.Context) *sessionLookup {
	l, _ := ctx.Value(installedContextKey).(*sessionLookup)
	return l
}

// FindCtx returns the data of the session with token. It also records the
// session user in the lookup of ctx.
func (s sessionStore) FindCtx(ctx context.Context, token string) ([]byte, bool, error) {
	var data []byte
	var userID, version *int64
	var email, role *string
	err := s.a.pool.QueryRow(ctx, `SELECT s.data, s.user_id, u.email, u.role, u.auth_version FROM sessions s LEFT JOIN users u ON u.id = s.user_id WHERE s.token = $1 AND current_timestamp < s.expiry`, token).
		Scan(&data, &userID, &email, &role, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, schemaHint(err)
	}
	if l := lookupFrom(ctx); l != nil && userID != nil {
		*l = sessionLookup{done: true, userID: *userID, found: email != nil}
		if l.found {
			l.user = User{ID: *userID, Email: *email, Role: *role}
			l.version = *version
		}
	}
	return data, true, nil
}

// CommitCtx inserts or updates the session with token. It decodes data with
// the codec of the session manager to write the user ID into sessions.user_id.
func (s sessionStore) CommitCtx(ctx context.Context, token string, data []byte, expiry time.Time) error {
	_, values, err := s.a.sessions.Codec.Decode(data)
	if err != nil {
		return err
	}
	var userID *int64
	if id, ok := values[sessionUserKey].(int64); ok {
		userID = &id
	}
	_, err = s.a.pool.Exec(ctx, `INSERT INTO sessions (token, data, expiry, user_id) VALUES ($1, $2, $3, $4) ON CONFLICT (token) DO UPDATE SET data = EXCLUDED.data, expiry = EXCLUDED.expiry, user_id = EXCLUDED.user_id`, token, data, expiry, userID)
	return schemaHint(err)
}

// DeleteCtx deletes the session with token.
func (s sessionStore) DeleteCtx(ctx context.Context, token string) error {
	_, err := s.a.pool.Exec(ctx, `DELETE FROM sessions WHERE token = $1`, token)
	return err
}

// Find is FindCtx without a request context. scs uses the context methods.
func (s sessionStore) Find(token string) ([]byte, bool, error) {
	return s.FindCtx(context.Background(), token)
}

// Commit is CommitCtx without a request context. scs uses the context methods.
func (s sessionStore) Commit(token string, data []byte, expiry time.Time) error {
	return s.CommitCtx(context.Background(), token, data, expiry)
}

// Delete is DeleteCtx without a request context. scs uses the context methods.
func (s sessionStore) Delete(token string) error {
	return s.DeleteCtx(context.Background(), token)
}

// schemaHint adds a Try line to the error of a query that needs a column of
// auth migration 00007 on a database without it.
func schemaHint(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42703" { // undefined_column
		return fmt.Errorf("auth: the database lacks a column of the auth migration 00007_users_auth_version.sql: %w\nTry: run spinup db migrate up, then start the API again", err)
	}
	return err
}
