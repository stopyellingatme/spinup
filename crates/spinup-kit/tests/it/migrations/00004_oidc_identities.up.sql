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
