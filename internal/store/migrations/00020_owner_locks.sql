-- +goose Up
-- One row per owner that a per-person cap counts for. Checking a cap upserts
-- the owner's row first, which takes that row's lock until the transaction
-- ends, so two writes counting the same owner run one after the other. A row
-- of people would not do: an owner need not have one, and an UPDATE of a
-- missing row locks nothing.
CREATE TABLE owner_locks (
    owner TEXT PRIMARY KEY
);

-- +goose Down
DROP TABLE owner_locks;
