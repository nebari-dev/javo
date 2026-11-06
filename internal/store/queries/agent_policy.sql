-- name: UpsertAgentPolicy :exec
INSERT INTO agent_policy (id, trusted_off, updated_at) VALUES (1, $1, $2)
ON CONFLICT (id) DO UPDATE SET trusted_off = excluded.trusted_off, updated_at = excluded.updated_at;

-- name: GetAgentPolicy :one
SELECT trusted_off, updated_at FROM agent_policy WHERE id = 1;
