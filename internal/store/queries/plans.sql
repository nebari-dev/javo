-- name: InsertPlan :exec
INSERT INTO plans (id, owner, source, state, body, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetPlan :one
SELECT * FROM plans WHERE id = $1;

-- Moves a plan from one state to another; no row means it was not in that
-- state (another request decided it first).
-- name: TransitionPlan :execrows
UPDATE plans SET state = $1, error = $2, decided_at = $3
WHERE id = $4 AND state = $5;

-- name: DeleteExpiredPlans :execrows
DELETE FROM plans WHERE expires_at < $1;

-- Adds tokens to the person's count for the day, only while the result stays
-- within the limit; no row means the allowance is used up.
-- name: ReserveAIUsage :one
INSERT INTO ai_usage (person, day, tokens) VALUES ($1, $2, $3)
ON CONFLICT (person, day) DO UPDATE SET tokens = ai_usage.tokens + excluded.tokens
WHERE ai_usage.tokens + excluded.tokens <= sqlc.arg(max_tokens)
RETURNING tokens;

-- name: ReleaseAIUsage :exec
UPDATE ai_usage SET tokens = CASE WHEN tokens > $1 THEN tokens - $1 ELSE 0 END
WHERE person = $2 AND day = $3;

-- name: GetAIUsage :one
SELECT tokens FROM ai_usage WHERE person = $1 AND day = $2;
