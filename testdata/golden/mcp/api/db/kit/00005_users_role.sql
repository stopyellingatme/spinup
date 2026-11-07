-- +goose Up
ALTER TABLE users ADD COLUMN role text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE users DROP COLUMN role;
