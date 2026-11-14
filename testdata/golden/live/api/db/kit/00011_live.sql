-- +goose Up
-- spinup:safe
-- The live data module of ADR 0033. live_feeds holds the position of each feed.
-- live_outbox holds the patches of the last hour (the hub prunes it). An existing
-- migration never changes.
CREATE TABLE live_feeds (
    key text PRIMARY KEY CONSTRAINT live_feeds_key_check CHECK (key ~ '^[a-z][a-z0-9_]{0,47}(:[0-9]{1,18})?$'),
    -- The epoch is the creation time in milliseconds. A feed that is deleted and
    -- created again gets a larger epoch, so an old cursor never matches it.
    epoch bigint NOT NULL DEFAULT (extract(epoch FROM clock_timestamp()) * 1000)::bigint,
    seq bigint NOT NULL DEFAULT 0 CONSTRAINT live_feeds_seq_check CHECK (seq >= 0),
    updated_at timestamptz NOT NULL DEFAULT now()
) WITH (fillfactor = 70);

CREATE TABLE live_outbox (
    feed text NOT NULL REFERENCES live_feeds (key) ON DELETE CASCADE,
    seq bigint NOT NULL,
    changes jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (feed, seq)
);
CREATE INDEX live_outbox_created_at_idx ON live_outbox (created_at);

-- One NOTIFY for each feed in a statement. The payload is "<seq> <feed key>". A
-- notification is a hint: the hub reads the patch from live_outbox.
-- +goose StatementBegin
CREATE FUNCTION live_outbox_notify() RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
    r record;
BEGIN
    FOR r IN SELECT feed, max(seq) AS seq FROM new_rows GROUP BY feed LOOP
        PERFORM pg_notify('spinup_live', r.seq::text || ' ' || r.feed);
    END LOOP;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER live_outbox_notify AFTER INSERT ON live_outbox
    REFERENCING NEW TABLE AS new_rows
    FOR EACH STATEMENT EXECUTE FUNCTION live_outbox_notify();

-- +goose Down
DROP TRIGGER live_outbox_notify ON live_outbox;
DROP FUNCTION live_outbox_notify();
DROP TABLE live_outbox;
DROP TABLE live_feeds;
