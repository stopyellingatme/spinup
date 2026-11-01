-- +goose Up
CREATE TABLE orders (id bigserial PRIMARY KEY, title text NOT NULL);
-- +goose Down
DROP TABLE orders;
