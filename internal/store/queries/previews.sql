-- Rows of the previews of one app, deleted ones included.
-- name: ListAppChildren :many
SELECT * FROM apps WHERE parent_id = $1 ORDER BY pull_number, id;

-- The preview of one pull request, deleted or not.
-- name: GetAppChild :one
SELECT * FROM apps WHERE parent_id = $1 AND pull_number = $2;

-- Previews not yet marked deleted, for the parent's own delete.
-- name: ListLiveAppChildren :many
SELECT * FROM apps WHERE parent_id = $1 AND deleted_at IS NULL ORDER BY pull_number, id;

-- name: CountAppChildren :one
SELECT COUNT(*) FROM apps WHERE parent_id = $1;

-- The controller's own write: neither audited nor announced.
-- name: UpsertPreviewSync :exec
INSERT INTO app_previews (app_id, synced_at, sync) VALUES ($1, $2, $3)
ON CONFLICT (app_id) DO UPDATE SET synced_at = excluded.synced_at, sync = excluded.sync;

-- name: GetPreviewSync :one
SELECT synced_at, sync FROM app_previews WHERE app_id = $1;

-- A new hook replaces the old one: its id stops resolving at once.
-- name: UpsertPreviewHook :exec
INSERT INTO app_previews (app_id, hook_id, hook_secret) VALUES ($1, $2, $3)
ON CONFLICT (app_id) DO UPDATE SET hook_id = excluded.hook_id, hook_secret = excluded.hook_secret;

-- name: GetPreviewByHook :one
SELECT app_id, hook_secret FROM app_previews WHERE hook_id = $1;

-- name: GetPreviewPoke :one
SELECT poked_at FROM app_previews WHERE app_id = $1;

-- name: PokePreview :one
UPDATE app_previews SET poked_at = $1 WHERE hook_id = $2 RETURNING app_id;

-- name: CountPreviewHooks :one
SELECT COUNT(*) FROM app_previews WHERE app_id = $1 AND hook_id IS NOT NULL;
