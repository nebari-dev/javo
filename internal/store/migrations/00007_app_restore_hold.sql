-- +goose Up
-- Restored apps wait for an admin: restore_hold names the restore that brought
-- the app back (empty: not held); the controller starts nothing for it, not
-- even its home claim, until restore_release_at (Unix milliseconds) has
-- passed. NULL release time while held means not released yet.
ALTER TABLE apps ADD COLUMN restore_hold TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN restore_release_at BIGINT;

-- +goose Down
ALTER TABLE apps DROP COLUMN restore_release_at;
ALTER TABLE apps DROP COLUMN restore_hold;
