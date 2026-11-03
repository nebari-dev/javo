-- name: InsertSecret :execrows
INSERT INTO secrets (id, owner, name, note, ciphertext, version, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 1, $6, $6)
ON CONFLICT (owner, name) DO NOTHING;

-- name: GetSecret :one
SELECT id, owner, name, note, ciphertext, version, created_at, updated_at FROM secrets WHERE id = $1;

-- name: GetSecretByOwnerName :one
SELECT id, owner, name, note, ciphertext, version, created_at, updated_at FROM secrets
WHERE owner = $1 AND name = $2;

-- name: CountSecretsForOwner :one
SELECT COUNT(*) FROM secrets WHERE owner = $1;

-- name: ListSecretsForOwner :many
SELECT id, owner, name, note, version, created_at, updated_at FROM secrets
WHERE owner = $1 ORDER BY name;

-- Every secret, for rotation.
-- name: ListSecretCiphertexts :many
SELECT id, owner, name, ciphertext FROM secrets ORDER BY id;

-- A new value: the version moves, so apps that use it restart.
-- name: SetSecretValue :execrows
UPDATE secrets SET ciphertext = $1, version = version + 1, updated_at = $2 WHERE id = $3;

-- name: SetSecretNote :execrows
UPDATE secrets SET note = $1, updated_at = $2 WHERE id = $3;

-- The same value sealed again (rotation): the version stays, so no app
-- restarts, and a value replaced since it was read is left alone.
-- name: ResealSecret :execrows
UPDATE secrets SET ciphertext = $1 WHERE id = $2 AND ciphertext = $3;

-- name: DeleteSecret :execrows
DELETE FROM secrets WHERE id = $1;

-- name: DeleteSecretsForOwner :execrows
DELETE FROM secrets WHERE owner = $1;

-- The live apps (not marked deleted) using each of the owner's secrets.
-- name: ListSecretUsesForOwner :many
SELECT b.secret_id, a.id AS app_id, a.name AS app_name, a.spec AS app_spec
FROM app_secrets b
JOIN secrets s ON s.id = b.secret_id
JOIN apps a ON a.id = b.app_id
WHERE s.owner = $1 AND a.deleted_at IS NULL
ORDER BY a.name;

-- name: ListAppsUsingSecret :many
SELECT DISTINCT b.app_id FROM app_secrets b
JOIN apps a ON a.id = b.app_id
WHERE b.secret_id = $1 AND a.deleted_at IS NULL
ORDER BY b.app_id;

-- What an app gets, without the values.
-- name: ListAppSecretBindings :many
SELECT b.env, s.id AS secret_id, s.owner AS secret_owner, s.name AS secret_name, s.version AS secret_version
FROM app_secrets b JOIN secrets s ON s.id = b.secret_id
WHERE b.app_id = $1 ORDER BY b.env;

-- What an app gets, with the sealed values, for the controller.
-- name: ListAppSecretValues :many
SELECT b.env, s.id AS secret_id, s.owner AS secret_owner, s.name AS secret_name, s.version AS secret_version,
       s.ciphertext
FROM app_secrets b JOIN secrets s ON s.id = b.secret_id
WHERE b.app_id = $1 ORDER BY b.env;

-- The app's variables from secrets; its repository token (env '@git-token',
-- model.GitTokenEnv) is set apart.
-- name: DeleteAppSecrets :exec
DELETE FROM app_secrets WHERE app_id = $1 AND env <> '@git-token';

-- name: DeleteAppGitTokenSecret :execrows
DELETE FROM app_secrets WHERE app_id = $1 AND env = '@git-token';

-- name: GetAppGitTokenSecret :one
SELECT s.name FROM app_secrets b JOIN secrets s ON s.id = b.secret_id
WHERE b.app_id = $1 AND b.env = '@git-token';

-- name: InsertAppSecret :exec
INSERT INTO app_secrets (app_id, env, secret_id) VALUES ($1, $2, $3);

-- The live jobs (not marked deleted) using each of the owner's secrets.
-- name: ListSecretJobUsesForOwner :many
SELECT b.secret_id, j.id AS job_id, j.name AS job_name, j.spec AS job_spec
FROM job_secrets b
JOIN secrets s ON s.id = b.secret_id
JOIN jobs j ON j.id = b.job_id
WHERE s.owner = $1 AND j.deleted_at IS NULL
ORDER BY j.name;

-- name: ListJobsUsingSecret :many
SELECT DISTINCT b.job_id FROM job_secrets b
JOIN jobs j ON j.id = b.job_id
WHERE b.secret_id = $1 AND j.deleted_at IS NULL
ORDER BY b.job_id;

-- What a job's runs get, without the values.
-- name: ListJobSecretBindings :many
SELECT b.env, s.name AS secret_name
FROM job_secrets b JOIN secrets s ON s.id = b.secret_id
WHERE b.job_id = $1 ORDER BY b.env;

-- What a job's runs get, with the sealed values, for the controller.
-- name: ListJobSecretValues :many
SELECT b.env, s.id AS secret_id, s.owner AS secret_owner, s.name AS secret_name, s.version AS secret_version,
       s.ciphertext
FROM job_secrets b JOIN secrets s ON s.id = b.secret_id
WHERE b.job_id = $1 ORDER BY b.env;

-- name: DeleteJobSecrets :exec
DELETE FROM job_secrets WHERE job_id = $1;

-- name: InsertJobSecret :exec
INSERT INTO job_secrets (job_id, env, secret_id) VALUES ($1, $2, $3);

-- name: GetSecretKeyset :one
SELECT wrapped, public, version FROM secret_keysets WHERE id = 'data';

-- Only the first keyset is kept: a second controller that raced to make one
-- loses and reads the winner's.
-- name: InsertSecretKeyset :execrows
INSERT INTO secret_keysets (id, wrapped, public, version, updated_at) VALUES ('data', $1, $2, 1, $3)
ON CONFLICT (id) DO NOTHING;

-- name: UpdateSecretKeyset :execrows
UPDATE secret_keysets SET wrapped = $1, public = $2, version = version + 1, updated_at = $3
WHERE id = 'data' AND version = $4;

-- The secrets with no fingerprint under the key key_id for their current
-- value, with their sealed values, for the controller.
-- name: ListUnfingerprintedSecrets :many
SELECT s.id, s.owner, s.name, s.ciphertext, s.version FROM secrets s
LEFT JOIN secret_fingerprints f ON f.secret_id = s.id AND f.key_id = $1 AND f.version = s.version
WHERE f.secret_id IS NULL ORDER BY s.id;

-- name: UpsertSecretFingerprint :exec
INSERT INTO secret_fingerprints (secret_id, key_id, version, length, mac) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (secret_id) DO UPDATE SET key_id = excluded.key_id, version = excluded.version,
    length = excluded.length, mac = excluded.mac;

-- Every secret of the owner with its fingerprint, if it has one.
-- name: ListSecretFingerprintsForOwner :many
SELECT s.name, s.version, f.key_id, f.version AS fingerprint_version, f.length, f.mac
FROM secrets s LEFT JOIN secret_fingerprints f ON f.secret_id = s.id
WHERE s.owner = $1 ORDER BY s.name;
