-- +goose Up
CREATE TABLE todos (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title text NOT NULL,
    done boolean NOT NULL,
    due date,
    priority text NOT NULL CHECK (priority IN ('low', 'normal', 'high')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX todos_user_id_id_idx ON todos (user_id, id);

-- +goose Down
DROP TABLE todos;
