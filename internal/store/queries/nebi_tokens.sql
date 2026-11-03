-- name: UpsertNebiToken :exec
INSERT INTO app_nebi_tokens (app_id, sealed, expires_at, set_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (app_id) DO UPDATE SET sealed = excluded.sealed, expires_at = excluded.expires_at, set_at = excluded.set_at;

-- name: GetNebiToken :one
SELECT sealed, expires_at FROM app_nebi_tokens WHERE app_id = $1;
