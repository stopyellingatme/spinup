-- This fails if an OIDC account still has no password. Set passwords first.
ALTER TABLE users ALTER COLUMN password_hash SET NOT NULL;
DROP TABLE user_identities;
