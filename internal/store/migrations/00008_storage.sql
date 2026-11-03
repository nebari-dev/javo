-- +goose Up
-- Storage. The cluster's storage findings live in cluster_facts
-- (keys "storage" and "storage.probe"); the admin's settings are the platform
-- config, except the run-time requests below.

-- One home per owner and namespace: which claim holds the person's files and
-- how it is moving between modes. Only the controller writes it; every write
-- raises version and names the version it read.
CREATE TABLE homes (
    id TEXT PRIMARY KEY,
    namespace TEXT NOT NULL,
    owner TEXT NOT NULL,
    spec TEXT NOT NULL,
    status TEXT NOT NULL,
    version BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    UNIQUE (namespace, owner)
);
CREATE INDEX homes_owner ON homes (owner);

-- A folder shared by the members of one group, declared by an admin. The
-- name names its claim; group_name is the group verbatim.
CREATE TABLE shared_folders (
    id TEXT PRIMARY KEY,
    namespace TEXT NOT NULL,
    name TEXT NOT NULL,
    group_name TEXT NOT NULL,
    spec TEXT NOT NULL,
    status TEXT NOT NULL,
    version BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    UNIQUE (namespace, name)
);

-- What an admin asks of the storage at run time (key "requests": a probe of
-- a class, the scheduled move of homes), as JSON.
CREATE TABLE storage_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    version BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);

-- +goose Down
DROP TABLE storage_settings;
DROP TABLE shared_folders;
DROP TABLE homes;
