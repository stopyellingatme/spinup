-- Queries for the todos table. sqlc turns them into Go code in api/internal/store.
-- Run `spinup db sqlc` after you change this file or a migration.

-- name: ListTodos :many
SELECT id, title, done, due, priority, created_at, updated_at
FROM todos
ORDER BY id
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountTodos :one
SELECT count(*) FROM todos;

-- name: GetTodo :one
SELECT id, title, done, due, priority, created_at, updated_at
FROM todos
WHERE id = $1;

-- name: CreateTodo :one
INSERT INTO todos (title, done, due, priority)
VALUES ($1, $2, $3, $4)
RETURNING id, title, done, due, priority, created_at, updated_at;

-- name: UpdateTodo :one
UPDATE todos
SET title = $2, done = $3, due = $4, priority = $5, updated_at = now()
WHERE id = $1
RETURNING id, title, done, due, priority, created_at, updated_at;

-- name: DeleteTodo :execrows
DELETE FROM todos
WHERE id = $1;
