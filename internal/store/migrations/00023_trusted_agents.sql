-- +goose Up
-- A coding agent its owner lets act without asking: until trusted_until
-- (Unix milliseconds; NULL when it asks), its own API token acts with the
-- owner's read-write access instead of an agent's. Setting it is not a spec
-- edit: the version does not change and the app does not restart.
ALTER TABLE apps ADD COLUMN trusted_until BIGINT;

-- The platform admin's switch for that: trusted_off true refuses it
-- everywhere and ends it where it is on. One row or none; none means on.
CREATE TABLE agent_policy (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    trusted_off BOOLEAN NOT NULL,
    updated_at BIGINT NOT NULL
);

-- +goose Down
DROP TABLE agent_policy;
ALTER TABLE apps DROP COLUMN trusted_until;
