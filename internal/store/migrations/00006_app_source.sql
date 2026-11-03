-- +goose Up
-- Apps from a Git repository, their pull request previews and the hand-off of
-- a pasted repository token. The repository itself is part of the app's spec
-- JSON; only what is queried or written by another party is a column or table.

-- A preview is an apps row that points at its parent (NULL for every ordinary
-- app) and the pull request it follows (0 for an ordinary app). Deleting the
-- parent row deletes its previews: the backstop of the store's own cleanup.
ALTER TABLE apps ADD COLUMN parent_id TEXT REFERENCES apps (id) ON DELETE CASCADE;
ALTER TABLE apps ADD COLUMN pull_number BIGINT NOT NULL DEFAULT 0;
CREATE INDEX apps_parent ON apps (parent_id);
CREATE UNIQUE INDEX apps_parent_pull ON apps (parent_id, pull_number);

-- Per parent app. The gateway writes hook_id, hook_secret and poked_at (the
-- webhook is only a trigger); the controller writes synced_at and sync (JSON
-- model.PreviewSync, what the last look at the pull requests found).
CREATE TABLE app_previews (
    app_id TEXT PRIMARY KEY REFERENCES apps (id) ON DELETE CASCADE,
    hook_id TEXT UNIQUE,
    hook_secret TEXT NOT NULL DEFAULT '',
    poked_at BIGINT,
    synced_at BIGINT,
    sync TEXT NOT NULL DEFAULT '{}'
);

-- A row means the app has had a repository token. token holds the text only
-- until the controller copied it to a Secret and cleared it (generation says
-- which paste it copied), so no backup of this table keeps a token. removed
-- marks a token taken away: the row stays so generation only ever grows.
CREATE TABLE app_git_tokens (
    app_id TEXT PRIMARY KEY REFERENCES apps (id) ON DELETE CASCADE,
    token TEXT NOT NULL,
    generation BIGINT NOT NULL,
    removed BOOLEAN NOT NULL DEFAULT FALSE,
    set_at BIGINT NOT NULL
);

-- +goose Down
DROP TABLE app_git_tokens;
DROP TABLE app_previews;
DROP INDEX apps_parent_pull;
DROP INDEX apps_parent;
ALTER TABLE apps DROP COLUMN pull_number;
ALTER TABLE apps DROP COLUMN parent_id;
