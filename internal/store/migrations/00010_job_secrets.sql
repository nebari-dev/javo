-- +goose Up
-- Which secret a job's runs get as which environment variable (as
-- app_secrets for apps). The store refuses to delete a secret a live job
-- uses; the cascades only clean up after jobs and secrets that are gone.
CREATE TABLE job_secrets (
    job_id TEXT NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    env TEXT NOT NULL,
    secret_id TEXT NOT NULL REFERENCES secrets (id) ON DELETE CASCADE,
    PRIMARY KEY (job_id, env)
);
CREATE INDEX job_secrets_secret ON job_secrets (secret_id);

-- +goose Down
DROP INDEX job_secrets_secret;
DROP TABLE job_secrets;
