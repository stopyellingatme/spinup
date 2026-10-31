-- +goose Up
CREATE TABLE widgets (id bigint);
-- +goose Down
DROP TABLE widgets;
