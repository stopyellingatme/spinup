-- +goose Up
CREATE TABLE bulletins (id bigserial PRIMARY KEY, title text NOT NULL);
-- +goose Down
DROP TABLE bulletins;
