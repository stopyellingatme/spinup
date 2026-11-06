-- +goose Up
CREATE TABLE notes (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title text NOT NULL,
    api_token text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notes_user_id_id_idx ON notes (user_id, id);
CREATE TRIGGER notes_audit AFTER INSERT OR UPDATE OR DELETE ON notes FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'id', 'api_token');
CREATE TRIGGER notes_audit_truncate AFTER TRUNCATE ON notes FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();

-- +goose Down
DROP TRIGGER IF EXISTS notes_audit_truncate ON notes;
DROP TRIGGER IF EXISTS notes_audit ON notes;
DROP TABLE notes;
