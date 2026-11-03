-- +goose Up
-- Proposed changes waiting for a person to approve them (internal/plans).
-- id is the SHA-256 of the plan id handed out, so the stored key never
-- grants anything. body is the planned change as JSON. Times are Unix
-- milliseconds; decided_at is NULL while the plan is pending.
CREATE TABLE plans (
    id         TEXT PRIMARY KEY,
    owner      TEXT NOT NULL,
    source     TEXT NOT NULL,
    state      TEXT NOT NULL,
    body       TEXT NOT NULL,
    error      TEXT NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    decided_at BIGINT
);
CREATE INDEX plans_expires_at ON plans (expires_at);

-- Model tokens each person used per UTC day ("2026-11-01"), for the daily
-- allowance. One row per person and day; all replicas share it.
CREATE TABLE ai_usage (
    person TEXT NOT NULL,
    day    TEXT NOT NULL,
    tokens BIGINT NOT NULL,
    PRIMARY KEY (person, day)
);

-- +goose Down
DROP TABLE ai_usage;
DROP TABLE plans;
