-- The per-person counts the caps in limits.go are checked against.

-- Takes the person's row lock, so two writes that count the same person's
-- rows run one after the other (Postgres; SQLite has one writer anyway).
-- name: LockPerson :exec
UPDATE people SET name = name WHERE name = $1;

-- name: CountAppsByOwner :one
SELECT COUNT(*) FROM apps WHERE owner = $1 AND deleted_at IS NULL AND parent_id IS NULL;

-- name: CountRunningAppsByOwner :one
SELECT COUNT(*) FROM apps WHERE owner = $1 AND deleted_at IS NULL AND parent_id IS NULL AND paused = FALSE;

-- name: CountPreviewsByOwner :one
SELECT COUNT(*) FROM apps WHERE owner = $1 AND deleted_at IS NULL AND parent_id IS NOT NULL;

-- name: CountJobsByOwner :one
SELECT COUNT(*) FROM jobs WHERE owner = $1 AND deleted_at IS NULL;

-- Runs not finished: requested ones waiting for their Job and running ones.
-- name: CountActiveRunsByJob :one
SELECT COUNT(*) FROM job_runs WHERE job_id = $1 AND finished_at IS NULL;

-- name: CountActiveRunsByOwner :one
SELECT COUNT(*) FROM job_runs r JOIN jobs j ON j.id = r.job_id
WHERE j.owner = $1 AND j.deleted_at IS NULL AND r.finished_at IS NULL;

-- Personal tokens only (no app or job) that have not expired.
-- name: CountPersonalTokens :one
SELECT COUNT(*) FROM api_tokens t JOIN people p ON p.id = t.person_id
WHERE p.name = $1 AND t.app_id IS NULL AND t.job_id IS NULL
  AND (t.expires_at IS NULL OR t.expires_at > $2);

-- name: CountPendingPlans :one
SELECT COUNT(*) FROM plans WHERE owner = $1 AND state = $2 AND expires_at > $3;
