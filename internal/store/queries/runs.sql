-- A taken (job, handle) or (job, slot) inserts nothing: it is the same run
-- again.
-- name: InsertRun :execrows
INSERT INTO job_runs (id, job_id, handle, trigger, created_at, requested_at, scheduled_at,
                      started_at, finished_at, outcome, reason, message, output_path, outputs,
                      parameters, cancel_requested, notified_at, slot)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
ON CONFLICT DO NOTHING;

-- name: GetRun :one
SELECT * FROM job_runs WHERE id = $1;

-- name: GetRunByHandle :one
SELECT * FROM job_runs WHERE job_id = $1 AND handle = $2;

-- name: GetRunBySlot :one
SELECT * FROM job_runs WHERE job_id = $1 AND slot = $2;

-- name: ListRunsByJob :many
SELECT * FROM job_runs WHERE job_id = $1 ORDER BY created_at DESC, id DESC;

-- What the controller reports about a run. The gateway's cancel request and
-- notified_at are not overwritten (MarkRunNotified sets the latter).
-- name: UpdateRun :execrows
UPDATE job_runs
SET handle = $1, started_at = $2, finished_at = $3, outcome = $4, reason = $5,
    message = $6, output_path = $7, outputs = $8
WHERE id = $9;

-- name: MarkRunNotified :execrows
UPDATE job_runs SET notified_at = $1 WHERE id = $2;

-- name: RequestRunCancel :execrows
UPDATE job_runs SET cancel_requested = TRUE WHERE id = $1 AND finished_at IS NULL;

-- name: DeleteRun :execrows
DELETE FROM job_runs WHERE id = $1;

-- Finished runs whose failure the owner has not been told about, oldest
-- first. The three outcomes are the failed ones (model.RunOutcomeFailed,
-- TimedOut, Lost).
-- name: ListUnnotifiedRuns :many
SELECT * FROM job_runs
WHERE notified_at IS NULL AND finished_at IS NOT NULL AND outcome IN ($1, $2, $3)
ORDER BY created_at, id;
