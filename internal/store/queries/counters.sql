-- name: SeedCounter :exec
INSERT INTO id_counters (name, next) VALUES ($1, $2)
ON CONFLICT (name) DO NOTHING;

-- Hands out the next number at or above $1 (the first of the range) and
-- moves the counter past it. The row lock serializes concurrent callers.
-- name: AllocateID :one
UPDATE id_counters
SET next = CASE WHEN next < $1 THEN $1 + 1 ELSE next + 1 END
WHERE name = $2
RETURNING CAST(next - 1 AS BIGINT) AS allocated;
