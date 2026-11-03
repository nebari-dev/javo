-- +goose Up
-- A token made for a job, as app_id marks one made for an app: the gateway
-- keeps it from what an app's token may not do (its owner's secrets), and it
-- ends with the job. The controller replaces a job's token made before this
-- column, so none stays unmarked.
ALTER TABLE api_tokens ADD COLUMN job_id TEXT REFERENCES jobs (id) ON DELETE CASCADE;

-- +goose Down
ALTER TABLE api_tokens DROP COLUMN job_id;
