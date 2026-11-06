-- +goose Up
CREATE TABLE notes (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title text NOT NULL,
    project_id bigint NOT NULL,
    parent_id bigint,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT notes_project_id_fkey FOREIGN KEY (project_id, user_id) REFERENCES projects (id, user_id)
);
CREATE UNIQUE INDEX notes_user_id_id_idx ON notes (user_id, id);
CREATE INDEX notes_project_id_idx ON notes (project_id);
CREATE INDEX notes_parent_id_idx ON notes (parent_id);
ALTER TABLE notes ADD CONSTRAINT notes_parent_id_fkey FOREIGN KEY (parent_id, user_id) REFERENCES notes (id, user_id);

-- +goose Down
DROP TABLE notes;
