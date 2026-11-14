-- +goose Up
-- Accounts, sessions, and failed logins for kit auth (github.com/stopyellingatme/spinup/kit/auth).

-- Kit auth stores emails trimmed and lower-cased, and passwords as argon2id PHC strings.
CREATE TABLE users (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email text NOT NULL,
    password_hash text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_email_key UNIQUE (email)
);

-- The scs pgxstore schema. The token column holds a SHA-256 hash of the cookie token.
CREATE TABLE sessions (
    token text PRIMARY KEY,
    data bytea NOT NULL,
    expiry timestamptz NOT NULL
);

CREATE INDEX sessions_expiry_idx ON sessions (expiry);

-- One row for each failed login. A successful login deletes the rows of its email.
CREATE TABLE login_attempts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email text NOT NULL,
    attempted_at timestamptz NOT NULL
);

CREATE INDEX login_attempts_email_attempted_at_idx ON login_attempts (email, attempted_at);

-- +goose Down
DROP TABLE login_attempts;
DROP TABLE sessions;
DROP TABLE users;
