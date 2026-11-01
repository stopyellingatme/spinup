-- +goose Up
CREATE TRIGGER notes_audit AFTER INSERT OR UPDATE OR DELETE ON notes FOR EACH ROW EXECUTE FUNCTION audit_row_change('user_id', 'id', 'api_token');
CREATE TRIGGER notes_audit_truncate AFTER TRUNCATE ON notes FOR EACH STATEMENT EXECUTE FUNCTION audit_truncate();

-- +goose Down
DROP TRIGGER IF EXISTS notes_audit_truncate ON notes;
DROP TRIGGER IF EXISTS notes_audit ON notes;
