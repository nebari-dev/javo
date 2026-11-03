-- name: GetLocalAccount :one
SELECT * FROM local_accounts WHERE username = $1;

-- name: ListLocalAccounts :many
SELECT * FROM local_accounts ORDER BY username;

-- name: UpsertLocalAccount :exec
INSERT INTO local_accounts (username, hash, groups, person_id) VALUES ($1, $2, $3, $4)
ON CONFLICT (username) DO UPDATE
SET hash = excluded.hash, groups = excluded.groups, person_id = excluded.person_id;

-- name: DeleteLocalAccount :execrows
DELETE FROM local_accounts WHERE username = $1;
