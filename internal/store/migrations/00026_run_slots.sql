-- +goose Up
-- job_runs.k8s_job is now handle: the name the executor knows a run by,
-- empty until the run is started, and still the name of its run folder.
-- slot is the time of the schedule a run was started for (Unix
-- milliseconds, as scheduled_at), set on the runs the controller starts for
-- a schedule: a job has one run per slot, so a slot is never started twice.
-- Runs recorded before have none.
ALTER TABLE job_runs RENAME COLUMN k8s_job TO handle;
ALTER TABLE job_runs ADD COLUMN slot BIGINT;
DROP INDEX job_runs_k8s_job;
-- One run per handle: recording it again updates the same run.
CREATE UNIQUE INDEX job_runs_handle ON job_runs (job_id, handle) WHERE handle <> '';
CREATE UNIQUE INDEX job_runs_slot ON job_runs (job_id, slot) WHERE slot IS NOT NULL;

-- +goose Down
DROP INDEX job_runs_slot;
DROP INDEX job_runs_handle;
ALTER TABLE job_runs DROP COLUMN slot;
ALTER TABLE job_runs RENAME COLUMN handle TO k8s_job;
CREATE UNIQUE INDEX job_runs_k8s_job ON job_runs (job_id, k8s_job) WHERE k8s_job <> '';
