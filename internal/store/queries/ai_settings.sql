-- name: UpsertAISetting :exec
INSERT INTO ai_settings (id, provider, base_url, model, key_sealed, key_last4, rev, updated_at)
VALUES (1, $1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (id) DO UPDATE SET provider = excluded.provider, base_url = excluded.base_url, model = excluded.model,
    key_sealed = excluded.key_sealed, key_last4 = excluded.key_last4, rev = excluded.rev, updated_at = excluded.updated_at;

-- name: GetAISetting :one
SELECT provider, base_url, model, key_sealed, key_last4, rev, updated_at FROM ai_settings WHERE id = 1;

-- name: DeleteAISetting :execrows
DELETE FROM ai_settings WHERE id = 1;
