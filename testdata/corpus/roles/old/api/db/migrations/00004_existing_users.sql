-- +goose Up
INSERT INTO users (email, password_hash) VALUES ('one@example.com', 'unused'), ('two@example.com', 'unused');
-- +goose Down
DELETE FROM users WHERE email IN ('one@example.com', 'two@example.com');
