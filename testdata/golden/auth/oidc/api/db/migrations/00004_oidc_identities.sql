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

-- The audit module (ADR 0025) audits user_identities. When OIDC comes after the
-- audit migration, this migration runs later and attaches the triggers itself.
-- Without the audit module, audit_row_change does not exist and the block does nothing.
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regprocedure('public.audit_row_change()') IS NOT NULL THEN
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = 'public.user_identities'::regclass AND tgname = 'user_identities_audit') THEN
            CREATE TRIGGER user_identities_audit AFTER INSERT OR UPDATE OR DELETE ON public.user_identities
                FOR EACH ROW EXECUTE FUNCTION audit_row_change('', 'provider,subject');
        END IF;
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = 'public.user_identities'::regclass AND tgname = 'user_identities_audit_truncate') THEN
            CREATE TRIGGER user_identities_audit_truncate AFTER TRUNCATE ON public.user_identities
                FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();
        END IF;
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
-- This fails transactionally if an OIDC account still has no password.
-- Set passwords before reverting the migration. Never delete accounts here.
ALTER TABLE users ALTER COLUMN password_hash SET NOT NULL;
DROP TRIGGER IF EXISTS user_identities_audit_truncate ON user_identities;
DROP TRIGGER IF EXISTS user_identities_audit ON user_identities;
DROP TABLE user_identities;
