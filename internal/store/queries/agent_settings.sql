-- name: UpsertAgentSetting :exec
INSERT INTO agent_settings (owner, base_url, model, key_last4, updated_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (owner) DO UPDATE SET base_url = excluded.base_url, model = excluded.model,
    key_last4 = excluded.key_last4, updated_at = excluded.updated_at;

-- name: GetAgentSetting :one
SELECT * FROM agent_settings WHERE owner = $1;

-- name: DeleteAgentSetting :exec
DELETE FROM agent_settings WHERE owner = $1;
