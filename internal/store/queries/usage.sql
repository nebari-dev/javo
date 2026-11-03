-- name: InsertUsage :exec
INSERT INTO usage_intervals (id, app_id, owner, template, size, started_at)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: CloseUsage :execrows
UPDATE usage_intervals SET stopped_at = $1 WHERE app_id = $2 AND stopped_at IS NULL;

-- name: ListUsageByApp :many
SELECT * FROM usage_intervals WHERE app_id = $1 ORDER BY started_at, id;

-- Intervals that overlap the window from $1 to now: still running, or stopped
-- after $1.
-- name: ListUsageSince :many
SELECT * FROM usage_intervals WHERE stopped_at IS NULL OR stopped_at > $1 ORDER BY started_at, id;
