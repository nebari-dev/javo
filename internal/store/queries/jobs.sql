-- name: InsertJob :execrows
INSERT INTO jobs (id, namespace, name, owner, template, paused, spec, version, created_at,
                  status, observed_version, size, cpu_millis, memory_bytes)
VALUES ($1, $2, $3, $4, $5, $6, $7, 1, $8, '{}', 0, $9, $10, $11)
ON CONFLICT (owner, name) DO NOTHING;

-- name: GetJob :one
SELECT * FROM jobs WHERE id = $1;

-- name: GetJobByOwnerName :one
SELECT * FROM jobs WHERE owner = $1 AND name = $2;

-- name: ListJobsByOwner :many
SELECT * FROM jobs WHERE owner = $1 AND deleted_at IS NULL ORDER BY created_at DESC, id DESC;

-- name: ListAllJobs :many
SELECT * FROM jobs ORDER BY created_at DESC, id DESC;

-- name: UpdateJobSpec :execrows
UPDATE jobs
SET template = $1, paused = $2, spec = $3, size = $4, cpu_millis = $5, memory_bytes = $6, version = version + 1
WHERE id = $7 AND version = $8 AND deleted_at IS NULL;

-- name: MarkJobDeleted :execrows
UPDATE jobs SET deleted_at = $1, version = version + 1
WHERE id = $2 AND deleted_at IS NULL;

-- Removes the row and its runs once it is marked deleted.
-- name: PurgeJob :execrows
DELETE FROM jobs WHERE id = $1 AND deleted_at IS NOT NULL;

-- name: SetJobStatus :execrows
UPDATE jobs SET status = $1, observed_version = $2, status_at = $3 WHERE id = $4;
