-- +goose Up
-- The pixi.toml and pixi.lock an app or a job runs in, copied from an outside
-- environment manager (such as Nebi) when its creator picked the environment.
-- The spec names the environment and the digest of these files; the files
-- live here so the lists of apps and jobs never read them. The controller
-- hands them to the pod that installs the environment.
CREATE TABLE app_environments (
    app_id TEXT PRIMARY KEY REFERENCES apps (id) ON DELETE CASCADE,
    manifest TEXT NOT NULL,
    lock TEXT NOT NULL
);

CREATE TABLE job_environments (
    job_id TEXT PRIMARY KEY REFERENCES jobs (id) ON DELETE CASCADE,
    manifest TEXT NOT NULL,
    lock TEXT NOT NULL
);

-- +goose Down
DROP TABLE job_environments;
DROP TABLE app_environments;
