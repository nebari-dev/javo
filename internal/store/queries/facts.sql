-- name: UpsertFact :exec
INSERT INTO cluster_facts (key, value, observed_at) VALUES ($1, $2, $3)
ON CONFLICT (key) DO UPDATE SET value = excluded.value, observed_at = excluded.observed_at;

-- name: GetFact :one
SELECT * FROM cluster_facts WHERE key = $1;

-- name: ListFacts :many
SELECT * FROM cluster_facts ORDER BY key;
