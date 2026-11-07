-- +goose Up
-- spinup:safe
CREATE TABLE api_keys (
    id bigserial PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    scope text NOT NULL CHECK (scope IN ('read', 'write')),
    token_hash bytea NOT NULL UNIQUE,
    display text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    expires_at timestamptz,
    revoked_at timestamptz
);
CREATE INDEX api_keys_user_id_idx ON api_keys (user_id);

CREATE TABLE api_key_plans (
    user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    plan text NOT NULL CHECK (plan ~ '^[a-z][a-z0-9_]{0,31}$'),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- One counter for each key and UTC day. The flush loop of kit/x/apikeys adds to it.
CREATE TABLE api_key_usage (
    key_id bigint NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    day date NOT NULL,
    count bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (key_id, day)
);

-- The audit module records key and plan changes, but not the usage counters. The block
-- acts only when the audit functions exist (the audit migration ran first). When the
-- audit module comes later, the doctor check apikeys-wired prints the attach statements
-- (ADR 0030 decision 5.1).
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regprocedure('public.audit_row_change()') IS NOT NULL THEN
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = to_regclass('public.api_keys') AND tgname = 'api_keys_audit') THEN
            CREATE TRIGGER api_keys_audit AFTER INSERT OR UPDATE OR DELETE ON public.api_keys
                FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'id', 'token_hash');
        END IF;
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = to_regclass('public.api_keys') AND tgname = 'api_keys_audit_truncate') THEN
            CREATE TRIGGER api_keys_audit_truncate AFTER TRUNCATE ON public.api_keys
                FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();
        END IF;
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = to_regclass('public.api_key_plans') AND tgname = 'api_key_plans_audit') THEN
            CREATE TRIGGER api_key_plans_audit AFTER INSERT OR UPDATE OR DELETE ON public.api_key_plans
                FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'user_id');
        END IF;
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = to_regclass('public.api_key_plans') AND tgname = 'api_key_plans_audit_truncate') THEN
            CREATE TRIGGER api_key_plans_audit_truncate AFTER TRUNCATE ON public.api_key_plans
                FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();
        END IF;
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE api_key_usage;
DROP TABLE api_key_plans;
DROP TABLE api_keys;
