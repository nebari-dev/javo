-- +goose Up
-- The values a workload reads (its API token, its secrets, its repository
-- token), for executors that have no Kubernetes Secret to keep them in. One
-- row per bundle, named like the Secret it stands for: unique in its
-- namespace. workload_id is the app or job it is kept for and owner the
-- person; meta is the bundle's notes as a JSON object. sealed is the values,
-- sealed with the public half of the secrets keyset (internal/secrets) for
-- this namespace, name and workload_id, so the database never holds one in
-- clear and a row copied to another workload does not open. updated_at is
-- Unix milliseconds.
CREATE TABLE workload_bundles (
    namespace   TEXT NOT NULL,
    name        TEXT NOT NULL,
    workload_id TEXT NOT NULL,
    owner       TEXT NOT NULL,
    meta        TEXT NOT NULL,
    sealed      BYTEA NOT NULL,
    updated_at  BIGINT NOT NULL,
    PRIMARY KEY (namespace, name)
);

-- +goose Down
DROP TABLE workload_bundles;
