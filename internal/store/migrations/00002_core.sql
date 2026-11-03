-- +goose Up
-- Every piece of javo data. Times are integer Unix milliseconds (dbtime), ids
-- are UUID v7 text, JSON columns are TEXT so both engines read them alike.
-- Columns that are queried are columns; the rest of a record is a JSON spec.

-- Next free number of a counter (uids, gids). One row per counter: the row
-- lock serializes concurrent allocations on Postgres, the write lock on SQLite.
CREATE TABLE id_counters (
    name TEXT PRIMARY KEY,
    next BIGINT NOT NULL
);

CREATE TABLE people (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    issuer TEXT NOT NULL,
    subject TEXT NOT NULL,
    username TEXT NOT NULL,
    email TEXT NOT NULL,
    display_name TEXT NOT NULL,
    -- Lower-cased copies written by the application, so prefix search gives
    -- the same answer on both engines.
    username_lower TEXT NOT NULL,
    email_lower TEXT NOT NULL,
    uid BIGINT UNIQUE,
    groups TEXT NOT NULL,
    last_login_at BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    UNIQUE (issuer, subject)
);

-- More provider accounts that are the same person.
CREATE TABLE person_subjects (
    person_id TEXT NOT NULL REFERENCES people (id) ON DELETE CASCADE,
    issuer TEXT NOT NULL,
    subject TEXT NOT NULL,
    PRIMARY KEY (issuer, subject)
);

CREATE TABLE groups (
    name TEXT PRIMARY KEY,
    gid BIGINT NOT NULL UNIQUE
);

CREATE TABLE local_accounts (
    username TEXT PRIMARY KEY,
    hash TEXT NOT NULL,
    groups TEXT NOT NULL,
    person_id TEXT REFERENCES people (id) ON DELETE SET NULL
);

-- id is the hex SHA-256 of the bearer token, so the token is never stored.
-- person is the person's name (the value apps carry as owner), also for local
-- accounts that have no people row. principal is JSON the session layer owns;
-- the store does not read it.
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    parent_id TEXT REFERENCES sessions (id) ON DELETE CASCADE,
    person TEXT NOT NULL,
    principal TEXT NOT NULL,
    provider TEXT NOT NULL,
    workspace TEXT NOT NULL,
    refresh TEXT NOT NULL,
    id_token TEXT NOT NULL,
    expires_at BIGINT NOT NULL,
    revalidate_at BIGINT NOT NULL,
    version BIGINT NOT NULL
);
CREATE INDEX sessions_person ON sessions (person);
CREATE INDEX sessions_parent ON sessions (parent_id);
CREATE INDEX sessions_expires ON sessions (expires_at);

CREATE TABLE templates (
    name TEXT PRIMARY KEY,
    source TEXT NOT NULL,
    spec TEXT NOT NULL,
    version BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);

-- spec columns (written by the gateway, guarded by version) and status
-- columns (written by the controller, never bumping version).
CREATE TABLE apps (
    id TEXT PRIMARY KEY,
    namespace TEXT NOT NULL,
    name TEXT NOT NULL,
    -- naming.ObjectName(owner, name): the name of the app's Pod and Service,
    -- and the label of its address when apps have their own host.
    object_name TEXT NOT NULL UNIQUE,
    owner TEXT NOT NULL,
    template TEXT NOT NULL,
    size TEXT NOT NULL,
    paused BOOLEAN NOT NULL,
    access TEXT NOT NULL,
    spec TEXT NOT NULL,
    version BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    restart_requested_at BIGINT,
    -- id of the app's API token that its owner revoked last; the controller
    -- makes the app a new token when the one it holds is this one.
    token_revoked TEXT NOT NULL DEFAULT '',
    deleted_at BIGINT,
    status TEXT NOT NULL,
    observed_version BIGINT NOT NULL,
    status_at BIGINT,
    UNIQUE (owner, name)
);
CREATE INDEX apps_owner ON apps (owner);

CREATE TABLE app_shares (
    app_id TEXT NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    value TEXT NOT NULL,
    PRIMARY KEY (app_id, kind, value)
);
CREATE INDEX app_shares_value ON app_shares (kind, value);

-- Personal tokens have no app; a token made for one app ends with it.
CREATE TABLE api_tokens (
    id TEXT PRIMARY KEY,
    person_id TEXT NOT NULL REFERENCES people (id) ON DELETE CASCADE,
    app_id TEXT REFERENCES apps (id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    scope TEXT NOT NULL,
    hash TEXT NOT NULL UNIQUE,
    prefix TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    last_used_at BIGINT,
    expires_at BIGINT,
    revoked_at BIGINT
);
CREATE INDEX api_tokens_person ON api_tokens (person_id);

CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    namespace TEXT NOT NULL,
    name TEXT NOT NULL,
    owner TEXT NOT NULL,
    template TEXT NOT NULL,
    paused BOOLEAN NOT NULL,
    spec TEXT NOT NULL,
    version BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    deleted_at BIGINT,
    status TEXT NOT NULL,
    observed_version BIGINT NOT NULL,
    status_at BIGINT,
    UNIQUE (owner, name)
);
CREATE INDEX jobs_owner ON jobs (owner);

-- A run requested by a person has an empty outcome until the controller
-- starts it.
CREATE TABLE job_runs (
    id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    k8s_job TEXT NOT NULL,
    trigger TEXT NOT NULL,
    created_at BIGINT NOT NULL,
    requested_at BIGINT,
    scheduled_at BIGINT,
    started_at BIGINT,
    finished_at BIGINT,
    outcome TEXT NOT NULL,
    reason TEXT NOT NULL,
    message TEXT NOT NULL,
    output_path TEXT NOT NULL,
    outputs TEXT NOT NULL,
    parameters TEXT NOT NULL,
    cancel_requested BOOLEAN NOT NULL,
    notified_at BIGINT
);
CREATE INDEX job_runs_job ON job_runs (job_id, created_at);
-- One run per Kubernetes Job: recording it again updates the same run.
CREATE UNIQUE INDEX job_runs_k8s_job ON job_runs (job_id, k8s_job) WHERE k8s_job <> '';

CREATE TABLE audit_events (
    id TEXT PRIMARY KEY,
    at BIGINT NOT NULL,
    actor TEXT NOT NULL,
    via TEXT NOT NULL,
    action TEXT NOT NULL,
    object_kind TEXT NOT NULL,
    object_name TEXT NOT NULL,
    -- Apps and jobs are unique only per namespace and name: object_id,
    -- object_namespace and object_owner say which one (empty for kinds keyed
    -- by name alone).
    object_id TEXT NOT NULL,
    object_namespace TEXT NOT NULL,
    object_owner TEXT NOT NULL,
    allowed BOOLEAN NOT NULL,
    detail TEXT NOT NULL
);
CREATE INDEX audit_events_at ON audit_events (at);

-- No foreign key: usage outlives the app it measured.
CREATE TABLE usage_intervals (
    id TEXT PRIMARY KEY,
    app_id TEXT NOT NULL,
    owner TEXT NOT NULL,
    template TEXT NOT NULL,
    size TEXT NOT NULL,
    started_at BIGINT NOT NULL,
    stopped_at BIGINT
);
CREATE INDEX usage_intervals_app ON usage_intervals (app_id);

CREATE TABLE cluster_facts (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    observed_at BIGINT NOT NULL
);

-- +goose Down
DROP TABLE cluster_facts;
DROP TABLE usage_intervals;
DROP TABLE audit_events;
DROP TABLE job_runs;
DROP TABLE jobs;
DROP TABLE api_tokens;
DROP TABLE app_shares;
DROP TABLE apps;
DROP TABLE templates;
DROP TABLE sessions;
DROP TABLE local_accounts;
DROP TABLE groups;
DROP TABLE person_subjects;
DROP TABLE people;
DROP TABLE id_counters;
