-- +goose Up
CREATE TABLE todos (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title text NOT NULL,
    done boolean NOT NULL,
    due date,
    priority text NOT NULL CHECK (priority IN ('low', 'normal', 'high')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE todos;
