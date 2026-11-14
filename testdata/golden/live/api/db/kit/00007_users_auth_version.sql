-- +goose Up
-- Kit auth compares the auth version of a session with users.auth_version on each
-- request. A password reset or an OIDC link increments the version, so every
-- older session of that account stops working, also a login that raced the reset.
-- An existing migration never changes.
ALTER TABLE users ADD COLUMN auth_version bigint NOT NULL DEFAULT 1;

-- The session store of kit auth writes the user of a session into user_id, so one
-- query reads the session and its user. A session from before this migration has
-- no user_id, and kit auth then reads its user with a second query.
ALTER TABLE sessions ADD COLUMN user_id bigint;

-- +goose Down
ALTER TABLE sessions DROP COLUMN user_id;
ALTER TABLE users DROP COLUMN auth_version;
