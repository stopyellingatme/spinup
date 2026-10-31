-- +goose Up
CREATE TABLE todos (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title text NOT NULL,
    done boolean NOT NULL,
    due date,
    priority text NOT NULL CHECK (priority IN ('low', 'normal', 'high')),
    owner_id bigint NOT NULL REFERENCES members (id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX todos_owner_id_idx ON todos (owner_id);

-- +goose Down
DROP TABLE todos;
