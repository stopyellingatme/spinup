-- +goose Up
-- spinup:safe
CREATE TABLE feature_flags (
    key text PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9_]{0,47}$'),
    enabled boolean NOT NULL DEFAULT false,
    roles text[] NOT NULL DEFAULT '{}',
    percentage smallint NOT NULL DEFAULT 0 CHECK (percentage BETWEEN 0 AND 100),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE feature_flag_users (
    flag_key text NOT NULL REFERENCES feature_flags(key) ON DELETE CASCADE,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    enabled boolean NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (flag_key, user_id)
);

-- The audit module records flag changes. The block acts only when the audit functions
-- exist (the audit migration ran first). When the audit module comes later, the doctor
-- check flags-wired prints the attach statements (ADR 0029 decision 11.7).
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regprocedure('public.audit_row_change()') IS NOT NULL THEN
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = to_regclass('public.feature_flags') AND tgname = 'feature_flags_audit') THEN
            CREATE TRIGGER feature_flags_audit AFTER INSERT OR UPDATE OR DELETE ON public.feature_flags
                FOR EACH ROW EXECUTE FUNCTION audit_row_change('', 'key');
        END IF;
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = to_regclass('public.feature_flags') AND tgname = 'feature_flags_audit_truncate') THEN
            CREATE TRIGGER feature_flags_audit_truncate AFTER TRUNCATE ON public.feature_flags
                FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();
        END IF;
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = to_regclass('public.feature_flag_users') AND tgname = 'feature_flag_users_audit') THEN
            CREATE TRIGGER feature_flag_users_audit AFTER INSERT OR UPDATE OR DELETE ON public.feature_flag_users
                FOR EACH ROW EXECUTE FUNCTION audit_row_change('', 'flag_key,user_id');
        END IF;
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = to_regclass('public.feature_flag_users') AND tgname = 'feature_flag_users_audit_truncate') THEN
            CREATE TRIGGER feature_flag_users_audit_truncate AFTER TRUNCATE ON public.feature_flag_users
                FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();
        END IF;
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE feature_flag_users;
DROP TABLE feature_flags;
