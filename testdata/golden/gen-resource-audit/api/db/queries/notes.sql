-- Queries for the notes table. sqlc turns them into Go code in api/internal/store.
-- Run `spinup db sqlc` after you change this file or a migration.

-- name: ListNotes :many
SELECT sqlc.embed(notes), (SELECT count(*) FROM notes AS counted WHERE counted.user_id = sqlc.arg(user_id))::bigint AS total
FROM notes
WHERE user_id = sqlc.arg(user_id)
ORDER BY id
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: CountNotes :one
SELECT count(*) FROM notes WHERE user_id = $1;

-- name: GetNote :one
SELECT id, user_id, title, api_token, created_at, updated_at
FROM notes
WHERE id = $1 AND user_id = $2;

-- name: CreateNote :one
INSERT INTO notes (title, api_token, user_id)
VALUES ($1, $2, $3)
RETURNING id, user_id, title, api_token, created_at, updated_at;

-- name: UpdateNote :one
UPDATE notes
SET title = $2, api_token = $3, updated_at = now()
WHERE id = $1 AND user_id = $4
RETURNING id, user_id, title, api_token, created_at, updated_at;

-- name: DeleteNote :execrows
DELETE FROM notes
WHERE id = $1 AND user_id = $2;
