-- The kit job queue (a stand-in for the R0.8 schema). It follows the River
-- schema: an enum, the job table with its indexes, the queue table, the
-- leader table, and a NOTIFY trigger in PL/pgSQL. The kit migrator owns it
-- through its own table, _spinup_kit_migrations.
CREATE TYPE kit_job_state AS ENUM (
    'available', 'cancelled', 'completed', 'discarded', 'pending', 'retryable', 'running', 'scheduled'
);

CREATE TABLE kit_job (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    state kit_job_state NOT NULL DEFAULT 'available',
    attempt smallint NOT NULL DEFAULT 0,
    max_attempts smallint NOT NULL,
    attempted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    finalized_at timestamptz,
    scheduled_at timestamptz NOT NULL DEFAULT now(),
    priority smallint NOT NULL DEFAULT 1,
    args jsonb NOT NULL DEFAULT '{}',
    attempted_by text[],
    errors jsonb[],
    kind text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}',
    queue text NOT NULL DEFAULT 'default',
    tags varchar(255)[] NOT NULL DEFAULT '{}',
    unique_key bytea,
    CONSTRAINT finalized_or_finalized_at_null CHECK (
        (finalized_at IS NULL AND state NOT IN ('cancelled', 'completed', 'discarded'))
        OR (finalized_at IS NOT NULL AND state IN ('cancelled', 'completed', 'discarded'))
    ),
    CONSTRAINT priority_in_range CHECK (priority >= 1 AND priority <= 4),
    CONSTRAINT queue_length CHECK (char_length(queue) > 0 AND char_length(queue) < 128),
    CONSTRAINT kind_length CHECK (char_length(kind) > 0 AND char_length(kind) < 128)
);

CREATE INDEX kit_job_kind ON kit_job USING btree (kind);
CREATE INDEX kit_job_state_and_finalized_at_index ON kit_job USING btree (state, finalized_at)
    WHERE finalized_at IS NOT NULL;
CREATE INDEX kit_job_prioritized_fetching_index ON kit_job USING btree (state, queue, priority, scheduled_at, id);
CREATE INDEX kit_job_args_index ON kit_job USING GIN (args);
CREATE INDEX kit_job_metadata_index ON kit_job USING GIN (metadata);
CREATE UNIQUE INDEX kit_job_unique_key ON kit_job (unique_key)
    WHERE unique_key IS NOT NULL AND state <> 'cancelled' AND state <> 'discarded';

CREATE TABLE kit_queue (
    name text PRIMARY KEY NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    paused_at timestamptz,
    updated_at timestamptz NOT NULL
);

CREATE UNLOGGED TABLE kit_leader (
    elected_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    leader_id text NOT NULL,
    name text PRIMARY KEY DEFAULT 'default',
    CONSTRAINT name_length CHECK (name = 'default'),
    CONSTRAINT leader_id_length CHECK (char_length(leader_id) > 0 AND char_length(leader_id) < 128)
);

CREATE FUNCTION kit_job_notify() RETURNS TRIGGER AS $$
DECLARE
    payload json;
BEGIN
    IF NEW.state = 'available' THEN
        payload = json_build_object('queue', NEW.queue);
        PERFORM pg_notify('kit_insert', payload::text);
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER kit_notify
    AFTER INSERT ON kit_job
    FOR EACH ROW
    EXECUTE PROCEDURE kit_job_notify();
