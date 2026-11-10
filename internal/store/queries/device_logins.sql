-- name: InsertDeviceLogin :exec
INSERT INTO device_logins (id, user_code, code_hash, created_at, expires_at, last_poll_at, state, owner)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: GetDeviceLoginByUserCode :one
SELECT * FROM device_logins WHERE user_code = $1;

-- name: GetDeviceLoginByCodeHash :one
SELECT * FROM device_logins WHERE code_hash = $1;

-- Only a pending request is decided, once.
-- name: DecideDeviceLogin :execrows
UPDATE device_logins SET state = $1, owner = $2 WHERE id = $3 AND state = 'pending';

-- One poll takes the token: the one whose update moved approved to spent.
-- name: SpendDeviceLogin :execrows
UPDATE device_logins SET state = 'spent' WHERE id = $1 AND state = 'approved';

-- name: SetDeviceLoginPolled :exec
UPDATE device_logins SET last_poll_at = $1 WHERE id = $2;

-- name: SweepDeviceLogins :execrows
DELETE FROM device_logins WHERE expires_at < $1;
