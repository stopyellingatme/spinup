-- +goose Up
CREATE TABLE entries (id bigserial PRIMARY KEY, title text NOT NULL);
-- +goose Down
DROP TABLE entries;
