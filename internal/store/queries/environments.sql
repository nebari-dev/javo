-- name: InsertAppEnvironment :exec
INSERT INTO app_environments (app_id, manifest, lock) VALUES ($1, $2, $3);

-- name: GetAppEnvironment :one
SELECT manifest, lock FROM app_environments WHERE app_id = $1;

-- name: InsertJobEnvironment :exec
INSERT INTO job_environments (job_id, manifest, lock) VALUES ($1, $2, $3);

-- name: GetJobEnvironment :one
SELECT manifest, lock FROM job_environments WHERE job_id = $1;
