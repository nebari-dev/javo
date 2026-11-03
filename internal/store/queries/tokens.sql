-- person_id and app_id are looked up by the caller in the same transaction.
-- name: InsertAPIToken :exec
INSERT INTO api_tokens (id, person_id, app_id, job_id, name, scope, hash, prefix, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- The token's owner is the person's name; app columns and job_id are NULL for
-- a personal token.
-- name: GetAPIToken :one
SELECT t.id, t.name, t.scope, t.hash, t.prefix, t.created_at, t.last_used_at, t.expires_at,
       p.name AS person, t.app_id, a.namespace AS app_namespace,
       a.object_name AS app_object_name, a.name AS app_name, t.job_id
FROM api_tokens t
JOIN people p ON p.id = t.person_id
LEFT JOIN apps a ON a.id = t.app_id
WHERE t.id = $1;

-- name: ListAPITokensForPerson :many
SELECT t.id, t.name, t.scope, t.hash, t.prefix, t.created_at, t.last_used_at, t.expires_at,
       p.name AS person, t.app_id, a.namespace AS app_namespace,
       a.object_name AS app_object_name, a.name AS app_name, t.job_id
FROM api_tokens t
JOIN people p ON p.id = t.person_id
LEFT JOIN apps a ON a.id = t.app_id
WHERE p.name = $1
ORDER BY t.created_at, t.id;

-- Tokens made for an app, for the janitor to check against the app's API access.
-- name: ListAppAPITokens :many
SELECT t.id, t.name, t.scope, t.hash, t.prefix, t.created_at, t.last_used_at, t.expires_at,
       p.name AS person, t.app_id, a.namespace AS app_namespace,
       a.object_name AS app_object_name, a.name AS app_name, t.job_id
FROM api_tokens t
JOIN people p ON p.id = t.person_id
LEFT JOIN apps a ON a.id = t.app_id
WHERE t.app_id IS NOT NULL
ORDER BY t.created_at, t.id;

-- name: DeleteAPIToken :execrows
DELETE FROM api_tokens WHERE id = $1;

-- Every token of the app except those of scope $2 (no token has scope '').
-- name: DeleteAPITokensForApp :execrows
DELETE FROM api_tokens WHERE app_id = $1 AND scope <> $2;

-- name: DeleteAPITokensForPerson :execrows
DELETE FROM api_tokens WHERE person_id IN (SELECT p.id FROM people p WHERE p.name = $1);

-- Expired tokens, and tokens of apps marked deleted (the row's purge removes
-- the rest by cascade).
-- name: SweepAPITokens :execrows
DELETE FROM api_tokens
WHERE expires_at <= $1
   OR app_id IN (SELECT id FROM apps WHERE deleted_at IS NOT NULL);

-- $1 is the use time, $3 the latest recorded use old enough to be replaced.
-- name: TouchAPIToken :exec
UPDATE api_tokens SET last_used_at = $1
WHERE id = $2 AND (last_used_at IS NULL OR last_used_at <= $3);
