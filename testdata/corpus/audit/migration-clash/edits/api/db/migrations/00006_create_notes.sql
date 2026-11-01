-- +goose Up
CREATE TABLE notes (id bigserial PRIMARY KEY, title text NOT NULL);
-- +goose Down
DROP TABLE notes;
