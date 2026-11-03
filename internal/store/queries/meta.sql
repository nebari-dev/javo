-- name: GetMeta :one
SELECT value FROM meta WHERE key = $1;

-- name: SetMeta :exec
INSERT INTO meta (key, value, updated_at) VALUES ($1, $2, $3)
ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at;
