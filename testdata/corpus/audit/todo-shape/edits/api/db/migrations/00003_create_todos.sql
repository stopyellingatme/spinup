-- +goose Up
CREATE TABLE todos (id bigserial PRIMARY KEY, user_id bigint NOT NULL REFERENCES users(id), title text NOT NULL);
-- +goose Down
DROP TABLE todos;
