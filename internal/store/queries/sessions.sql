-- name: InsertSession :exec
INSERT INTO sessions (id, kind, parent_id, person, principal, provider, workspace,
                      refresh, id_token, expires_at, revalidate_at, version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 1);

-- name: GetSession :one
SELECT * FROM sessions WHERE id = $1;

-- name: UpdateSession :execrows
UPDATE sessions
SET principal = $1, refresh = $2, id_token = $3, expires_at = $4, revalidate_at = $5,
    version = version + 1
WHERE id = $6 AND version = $7;

-- Children (app sessions and tickets) go with their parent. Returns the row
-- it deleted: Delete audits a sign-out by its kind, and Redeem uses a ticket
-- once.
-- name: DeleteSession :one
DELETE FROM sessions WHERE id = $1 RETURNING *;

-- name: DeleteSessionsForPerson :execrows
-- Only ui sessions are counted and matched: their app sessions and tickets go
-- by cascade, and whether a cascaded row is counted differs between engines.
DELETE FROM sessions WHERE person = $1 AND kind = 'ui';

-- name: SweepSessions :execrows
DELETE FROM sessions WHERE expires_at < $1;
