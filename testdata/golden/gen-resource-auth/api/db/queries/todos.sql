-- Queries for the todos table. sqlc turns them into Go code in api/internal/store.
-- Run `spinup db sqlc` after you change this file or a migration.

-- name: ListTodos :many
SELECT id, user_id, title, done, due, priority, created_at, updated_at
FROM todos
WHERE user_id = sqlc.arg(user_id)
ORDER BY id
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountTodos :one
SELECT count(*) FROM todos WHERE user_id = $1;

-- name: GetTodo :one
SELECT id, user_id, title, done, due, priority, created_at, updated_at
FROM todos
WHERE id = $1 AND user_id = $2;

-- name: CreateTodo :one
INSERT INTO todos (title, done, due, priority, user_id)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, user_id, title, done, due, priority, created_at, updated_at;

-- name: UpdateTodo :one
UPDATE todos
SET title = $2, done = $3, due = $4, priority = $5, updated_at = now()
WHERE id = $1 AND user_id = $6
RETURNING id, user_id, title, done, due, priority, created_at, updated_at;

-- name: DeleteTodo :execrows
DELETE FROM todos
WHERE id = $1 AND user_id = $2;
