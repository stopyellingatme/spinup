-- +goose Up
-- The audit trail (ADR 0025). Triggers write one audit_events row for each
-- change of an audited table. An existing migration never changes.
--
-- The actor of a row change is the transaction-local setting spinup.audit, a
-- JSON object {"kind","id","email","role","source","reason","request_id"}.
-- The kit sets it with audit.Bind. A data migration sets it itself on its first line:
--   SELECT set_config('spinup.audit', '{"kind":"system","source":"migrate"}', true);
-- Without the setting, the event is unattributed with the source sql.
--
-- The guard trigger stops accidental UPDATE, DELETE, and TRUNCATE statements on
-- audit_events. It does not stop the database owner or the application user.

CREATE TABLE audit_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    action text NOT NULL CONSTRAINT audit_events_action_check CHECK (action ~ '^[a-z][a-z0-9_.]{0,63}$'),
    table_name text NOT NULL,
    record_id text NOT NULL,
    owner_id bigint,
    changes jsonb NOT NULL DEFAULT '{}',
    actor_kind text NOT NULL CONSTRAINT audit_events_actor_kind_check CHECK (actor_kind IN ('user', 'operator', 'system', 'unattributed')),
    actor_id bigint,
    actor_email text,
    actor_role text,
    source text NOT NULL,
    reason text,
    request_id text
);

CREATE INDEX audit_events_record_idx ON audit_events (table_name, record_id, id DESC);
CREATE INDEX audit_events_owner_idx ON audit_events (owner_id, table_name, record_id, id DESC);
CREATE INDEX audit_events_actor_idx ON audit_events (actor_id, id DESC) WHERE actor_id IS NOT NULL;
CREATE INDEX audit_events_occurred_at_idx ON audit_events (occurred_at);

-- audit_append is the one writer. It reads no setting.
-- +goose StatementBegin
CREATE FUNCTION audit_append(
    action text, table_name text, record_id text, owner_id bigint, changes jsonb,
    actor_kind text, actor_id bigint, actor_email text, actor_role text,
    source text, reason text, request_id text
) RETURNS bigint
LANGUAGE sql
SECURITY INVOKER
SET search_path = pg_catalog, public, pg_temp
AS $$
    INSERT INTO public.audit_events
        (action, table_name, record_id, owner_id, changes, actor_kind, actor_id, actor_email, actor_role, source, reason, request_id)
    VALUES
        ($1, $2, $3, $4, coalesce($5, '{}'::jsonb), coalesce(nullif($6, ''), 'unattributed'), $7, nullif($8, ''), nullif($9, ''),
         coalesce(nullif($10, ''), 'sql'), nullif($11, ''), nullif($12, ''))
    RETURNING id;
$$;
-- +goose StatementEnd

-- audit_actor returns the actor setting of the transaction, or {} when it is empty.
-- +goose StatementBegin
CREATE FUNCTION audit_actor() RETURNS jsonb
LANGUAGE plpgsql
STABLE
SECURITY INVOKER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    raw text := current_setting('spinup.audit', true);
    actor jsonb;
BEGIN
    IF raw IS NULL OR raw = '' THEN
        RETURN '{}'::jsonb;
    END IF;
    actor := raw::jsonb;
    IF jsonb_typeof(actor) <> 'object' THEN
        RAISE EXCEPTION 'audit: the setting spinup.audit is not a JSON object'
            USING HINT = 'Try: bind the actor with audit.Bind, or set a JSON object with set_config.';
    END IF;
    RETURN actor;
END
$$;
-- +goose StatementEnd

-- audit_capped replaces a value larger than 4096 bytes with its size and SHA-256.
-- +goose StatementBegin
CREATE FUNCTION audit_capped(v jsonb) RETURNS jsonb
LANGUAGE sql
IMMUTABLE
SECURITY INVOKER
SET search_path = pg_catalog, public, pg_temp
AS $$
    SELECT CASE
        WHEN octet_length($1 #>> '{}') > 4096 THEN jsonb_build_object(
            'truncated', true,
            'bytes', octet_length($1 #>> '{}'),
            'sha256', encode(sha256(convert_to($1 #>> '{}', 'UTF8')), 'hex'))
        ELSE $1
    END;
$$;
-- +goose StatementEnd

-- audit_row_change is the row trigger. Its arguments are the owner column ('' for
-- none), the key columns separated by commas ('' means id), then the redacted columns.
-- +goose StatementBegin
CREATE FUNCTION audit_row_change() RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    owner_col text := '';
    key_cols text[] := ARRAY['id'];
    redacted text[] := ARRAY[]::text[];
    skipped constant text[] := ARRAY['id', 'created_at', 'updated_at'];
    old_row jsonb;
    new_row jsonb;
    cur_row jsonb;
    diff jsonb;
    key_parts text[] := ARRAY[]::text[];
    col text;
    owner_value bigint;
    actor jsonb;
BEGIN
    IF TG_NARGS >= 1 THEN
        owner_col := TG_ARGV[0];
    END IF;
    IF TG_NARGS >= 2 AND TG_ARGV[1] <> '' THEN
        key_cols := string_to_array(TG_ARGV[1], ',');
    END IF;
    IF TG_NARGS >= 3 THEN
        redacted := TG_ARGV[2:TG_NARGS - 1];
    END IF;

    IF TG_OP <> 'INSERT' THEN
        old_row := to_jsonb(OLD);
    END IF;
    IF TG_OP <> 'DELETE' THEN
        new_row := to_jsonb(NEW);
    END IF;
    cur_row := coalesce(new_row, old_row);

    IF TG_OP = 'INSERT' THEN
        SELECT coalesce(jsonb_object_agg(n.key, CASE WHEN n.key = ANY (redacted)
                THEN jsonb_build_object('redacted', true)
                ELSE jsonb_build_object('new', audit_capped(n.value)) END), '{}'::jsonb)
            INTO diff
            FROM jsonb_each(new_row) AS n
            WHERE NOT n.key = ANY (skipped) AND jsonb_typeof(n.value) <> 'null';
    ELSIF TG_OP = 'UPDATE' THEN
        SELECT coalesce(jsonb_object_agg(n.key, CASE WHEN n.key = ANY (redacted)
                THEN jsonb_build_object('redacted', true)
                ELSE jsonb_build_object('old', audit_capped(old_row -> n.key), 'new', audit_capped(n.value)) END), '{}'::jsonb)
            INTO diff
            FROM jsonb_each(new_row) AS n
            WHERE NOT n.key = ANY (skipped) AND n.value IS DISTINCT FROM old_row -> n.key;
        IF diff = '{}'::jsonb THEN
            RETURN NULL;
        END IF;
    ELSE
        SELECT coalesce(jsonb_object_agg(o.key, CASE WHEN o.key = ANY (redacted)
                THEN jsonb_build_object('redacted', true)
                ELSE jsonb_build_object('old', audit_capped(o.value)) END), '{}'::jsonb)
            INTO diff
            FROM jsonb_each(old_row) AS o
            WHERE NOT o.key = ANY (skipped);
    END IF;

    FOREACH col IN ARRAY key_cols LOOP
        col := btrim(col);
        IF NOT cur_row ? col THEN
            RAISE EXCEPTION 'audit: the key column % is not a column of %', col, TG_TABLE_NAME
                USING HINT = 'Try: attach the trigger again with the key columns of the table.';
        END IF;
        key_parts := key_parts || coalesce(cur_row ->> col, '');
    END LOOP;

    IF owner_col <> '' THEN
        IF NOT cur_row ? owner_col THEN
            RAISE EXCEPTION 'audit: the owner column % is not a column of %', owner_col, TG_TABLE_NAME
                USING HINT = 'Try: attach the trigger again with the owner column of the table, or with an empty string.';
        END IF;
        owner_value := (cur_row ->> owner_col)::bigint;
    END IF;

    actor := audit_actor();
    PERFORM audit_append(
        CASE TG_OP WHEN 'INSERT' THEN 'create' ELSE lower(TG_OP) END, TG_TABLE_NAME, array_to_string(key_parts, ':'), owner_value, diff,
        actor ->> 'kind', (actor ->> 'id')::bigint, actor ->> 'email', actor ->> 'role',
        actor ->> 'source', actor ->> 'reason', actor ->> 'request_id');
    RETURN NULL;
END
$$;
-- +goose StatementEnd

-- audit_truncate writes one truncate event with the record_id '*'.
-- +goose StatementBegin
CREATE FUNCTION audit_truncate() RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    actor jsonb := audit_actor();
BEGIN
    PERFORM audit_append(
        'truncate', TG_TABLE_NAME, '*', NULL, '{}'::jsonb,
        actor ->> 'kind', (actor ->> 'id')::bigint, actor ->> 'email', actor ->> 'role',
        actor ->> 'source', actor ->> 'reason', actor ->> 'request_id');
    RETURN NULL;
END
$$;
-- +goose StatementEnd

-- audit_immutable is the guard. It allows a DELETE only inside audit_prune.
-- +goose StatementBegin
CREATE FUNCTION audit_immutable() RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
    IF TG_OP = 'DELETE' AND current_setting('spinup.audit_prune', true) = 'on' THEN
        RETURN NULL;
    END IF;
    RAISE EXCEPTION 'audit: % on audit_events is not allowed', TG_OP
        USING HINT = 'Try: remove old events with audit_prune(before, reason), or the app command audit prune.';
END
$$;
-- +goose StatementEnd

CREATE TRIGGER audit_events_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON audit_events
    FOR EACH STATEMENT EXECUTE FUNCTION audit_immutable();

-- audit_prune is the one delete path. It refuses an age below 1 day and records an audit.prune event.
-- +goose StatementBegin
CREATE FUNCTION audit_prune(before timestamptz, reason text) RETURNS bigint
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    removed bigint;
    actor jsonb;
BEGIN
    IF audit_prune.before IS NULL OR audit_prune.before > now() - interval '1 day' THEN
        RAISE EXCEPTION 'audit: cannot prune events younger than 1 day (before %)', audit_prune.before
            USING HINT = 'Try: pass a time at least 1 day in the past.';
    END IF;
    IF coalesce(btrim(audit_prune.reason), '') = '' THEN
        RAISE EXCEPTION 'audit: a prune needs a reason'
            USING HINT = 'Try: pass a reason, for example the retention rule.';
    END IF;
    PERFORM set_config('spinup.audit_prune', 'on', true);
    DELETE FROM public.audit_events WHERE occurred_at < audit_prune.before;
    GET DIAGNOSTICS removed = ROW_COUNT;
    PERFORM set_config('spinup.audit_prune', 'off', true);
    actor := audit_actor();
    PERFORM audit_append(
        'audit.prune', 'audit_events', '*', NULL,
        jsonb_build_object('before', to_char(audit_prune.before AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), 'count', removed),
        actor ->> 'kind', (actor ->> 'id')::bigint, actor ->> 'email', actor ->> 'role',
        actor ->> 'source', audit_prune.reason, actor ->> 'request_id');
    RETURN removed;
END
$$;
-- +goose StatementEnd

-- The module audits users. A missing table is skipped without an error.
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('public.users') IS NOT NULL THEN
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = to_regclass('public.users') AND tgname = 'users_audit') THEN
            CREATE TRIGGER users_audit AFTER INSERT OR UPDATE OR DELETE ON public.users
                FOR EACH ROW EXECUTE FUNCTION audit_row_change('', 'id', 'password_hash');
        END IF;
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = to_regclass('public.users') AND tgname = 'users_audit_truncate') THEN
            CREATE TRIGGER users_audit_truncate AFTER TRUNCATE ON public.users
                FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();
        END IF;
    END IF;
END
$$;
-- +goose StatementEnd

-- user_identities exists only with OIDC (auth 00004). Its key is (provider, subject).
-- Both orders attach the triggers: this block when 00004 ran first, and 00004 itself
-- when it runs after this migration. spinup g audit user_identities is needed only
-- for an app whose 00004 file came from a spinup version before this change.
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('public.user_identities') IS NOT NULL THEN
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = to_regclass('public.user_identities') AND tgname = 'user_identities_audit') THEN
            CREATE TRIGGER user_identities_audit AFTER INSERT OR UPDATE OR DELETE ON public.user_identities
                FOR EACH ROW EXECUTE FUNCTION audit_row_change('', 'provider,subject');
        END IF;
        IF NOT EXISTS (SELECT 1 FROM pg_trigger
                WHERE tgrelid = to_regclass('public.user_identities') AND tgname = 'user_identities_audit_truncate') THEN
            CREATE TRIGGER user_identities_audit_truncate AFTER TRUNCATE ON public.user_identities
                FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();
        END IF;
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS user_identities_audit_truncate ON user_identities;
DROP TRIGGER IF EXISTS user_identities_audit ON user_identities;
DROP TRIGGER IF EXISTS users_audit_truncate ON users;
DROP TRIGGER IF EXISTS users_audit ON users;
DROP TABLE audit_events;
DROP FUNCTION audit_prune(timestamptz, text);
DROP FUNCTION audit_immutable();
DROP FUNCTION audit_truncate();
DROP FUNCTION audit_row_change();
DROP FUNCTION audit_capped(jsonb);
DROP FUNCTION audit_actor();
DROP FUNCTION audit_append(text, text, text, bigint, jsonb, text, bigint, text, text, text, text, text);
