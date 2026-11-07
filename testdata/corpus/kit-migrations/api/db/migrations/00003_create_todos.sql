-- +goose Up
CREATE TABLE todos (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    title text NOT NULL
);

-- +goose Down
DROP TABLE todos;
