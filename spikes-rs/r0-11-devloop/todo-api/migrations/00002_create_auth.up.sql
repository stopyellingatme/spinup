-- Accounts, sessions, and failed logins for kit auth (the Rust form of the
-- auth pack migration 00002_create_auth.sql).

-- Kit auth stores emails trimmed and lower-cased, and passwords as argon2id PHC strings.
CREATE TABLE users (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email text NOT NULL,
    password_hash text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_email_key UNIQUE (email)
);

-- The tower-sessions store of kit auth. The token column holds the hex
-- SHA-256 of the cookie value. user_id copies auth.user_id from the data, so
-- the sessions of one user can be deleted with one indexed statement.
CREATE TABLE sessions (
    token text PRIMARY KEY,
    user_id bigint REFERENCES users(id) ON DELETE CASCADE,
    data jsonb NOT NULL,
    expiry timestamptz NOT NULL
);

CREATE INDEX sessions_expiry_idx ON sessions (expiry);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);

-- One row for each failed login. A successful login deletes the rows of its email.
CREATE TABLE login_attempts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email text NOT NULL,
    attempted_at timestamptz NOT NULL
);

CREATE INDEX login_attempts_email_attempted_at_idx ON login_attempts (email, attempted_at);
