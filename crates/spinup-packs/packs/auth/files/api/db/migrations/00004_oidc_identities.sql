-- +goose Up
-- A new migration follows password reset. Existing auth migrations never change.
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;

CREATE TABLE user_identities (
    provider text NOT NULL,
    subject text NOT NULL,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email text NOT NULL,
    PRIMARY KEY (provider, subject)
);
CREATE INDEX user_identities_user_id_idx ON user_identities (user_id);

-- +goose Down
-- This fails transactionally if an OIDC account still has no password.
-- Set passwords before reverting the migration. Never delete accounts here.
ALTER TABLE users ALTER COLUMN password_hash SET NOT NULL;
DROP TABLE user_identities;
