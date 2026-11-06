-- A taken (owner, name) or object name inserts nothing. A deleted app keeps its name
-- until it is purged.
-- name: InsertApp :execrows
INSERT INTO apps (id, namespace, name, owner, template, size, paused, access, spec, version,
                  created_at, restart_requested_at, status, observed_version, object_name,
                  cpu_millis, memory_bytes, idle_stop_minutes, parent_id, pull_number, restore_hold)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 1, $10, $11, '{}', 0, $12, $13, $14, $15, $16, $17, $18)
ON CONFLICT DO NOTHING;

-- name: GetApp :one
SELECT * FROM apps WHERE id = $1;

-- name: GetAppByOwnerName :one
SELECT * FROM apps WHERE owner = $1 AND name = $2;

-- name: GetAppByObjectName :one
SELECT * FROM apps WHERE object_name = $1;

-- Previews (rows with a parent) only when $2 is true.
-- name: ListAppsByOwner :many
SELECT * FROM apps WHERE owner = $1 AND deleted_at IS NULL AND (parent_id IS NULL OR CAST($2 AS BOOLEAN))
ORDER BY created_at DESC, id DESC;

-- Every app including deleted ones: the controller's resync.
-- name: ListAllApps :many
SELECT * FROM apps ORDER BY created_at DESC, id DESC;

-- Candidates for "shared with me": other people's apps listing the person or
-- the group. Whether the person may open one is authz.Decide's answer.
-- name: ListAppsSharedWith :many
SELECT a.* FROM apps a
JOIN app_shares s ON s.app_id = a.id
WHERE s.kind = $1 AND s.value = $2 AND a.owner <> $3 AND a.deleted_at IS NULL
  AND (a.parent_id IS NULL OR CAST($4 AS BOOLEAN))
ORDER BY a.created_at DESC, a.id DESC;

-- Candidates for "shared with me" open to everyone signed in or to anyone with
-- the link: other people's apps at those levels. Whether the person may open
-- one (the template's cap among others) is authz.Decide's answer.
-- name: ListAppsOpenToAll :many
SELECT * FROM apps
WHERE access IN ('SignedIn', 'Public') AND owner <> $1 AND deleted_at IS NULL
  AND (parent_id IS NULL OR CAST($2 AS BOOLEAN))
ORDER BY created_at DESC, id DESC;

-- Spec columns only; status columns are never touched here.
-- name: UpdateAppSpec :execrows
UPDATE apps
SET template = $1, size = $2, paused = $3, access = $4, spec = $5,
    restart_requested_at = $6, cpu_millis = $7, memory_bytes = $8, idle_stop_minutes = $9,
    version = version + 1
WHERE id = $10 AND version = $11 AND deleted_at IS NULL;

-- Neither the spec nor the version changes: this is not an edit.
-- name: SetAppTokenRevoked :execrows
UPDATE apps SET token_revoked = $1 WHERE id = $2 AND deleted_at IS NULL;

-- Traffic seen by a gateway; only ever moves forward, so replicas flushing in
-- any order keep the latest. Neither the spec nor the version changes.
-- name: NoteAppActivity :execrows
UPDATE apps SET last_active_at = $1
WHERE (last_active_at IS NULL OR last_active_at < $2) AND id = $3;

-- Running apps with a "Stop when idle" timeout: the controller's idle check.
-- name: ListIdleStopCandidates :many
SELECT * FROM apps
WHERE idle_stop_minutes > 0 AND paused = FALSE AND deleted_at IS NULL
ORDER BY id;

-- name: MarkAppDeleted :execrows
UPDATE apps SET deleted_at = $1, version = version + 1
WHERE id = $2 AND deleted_at IS NULL;

-- Takes a write lock on a live app without changing it, so a delete of the app
-- and an insert of a child cannot interleave. No row means gone or deleted.
-- name: LockLiveApp :execrows
UPDATE apps SET version = version WHERE id = $1 AND deleted_at IS NULL;

-- Removes the row (and with it its shares and tokens) once it is marked deleted.
-- name: PurgeApp :execrows
DELETE FROM apps WHERE id = $1 AND deleted_at IS NOT NULL;

-- Status columns only; version and the spec are never touched here.
-- name: SetAppStatus :execrows
UPDATE apps SET status = $1, observed_version = $2, status_at = $3 WHERE id = $4;

-- name: DeleteAppShares :exec
DELETE FROM app_shares WHERE app_id = $1;

-- name: InsertAppShare :exec
INSERT INTO app_shares (app_id, kind, value) VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING;

-- name: ListAppShares :many
SELECT * FROM app_shares WHERE app_id = $1 ORDER BY kind, value;

-- name: ListAppSharesByOwner :many
SELECT s.* FROM app_shares s JOIN apps a ON a.id = s.app_id
WHERE a.owner = $1 ORDER BY s.app_id, s.kind, s.value;

-- name: ListAllAppShares :many
SELECT * FROM app_shares ORDER BY app_id, kind, value;

-- Holds a live app under a restore's name, not yet released. The version is
-- raised so the controller hears of it.
-- name: HoldApp :execrows
UPDATE apps SET restore_hold = $1, restore_release_at = NULL, version = version + 1
WHERE id = $2 AND deleted_at IS NULL;

-- name: ListLiveAppIDs :many
SELECT id FROM apps WHERE deleted_at IS NULL ORDER BY id;

-- Apps still held at $1: not released, or released for later.
-- name: ListHeldApps :many
SELECT * FROM apps
WHERE restore_hold <> '' AND deleted_at IS NULL AND (restore_release_at IS NULL OR restore_release_at > $1)
ORDER BY restore_hold, name, id;

-- name: ListUnreleasedApps :many
SELECT * FROM apps
WHERE restore_hold = $1 AND restore_release_at IS NULL AND deleted_at IS NULL
ORDER BY name, id;

-- name: ReleaseApp :execrows
UPDATE apps SET restore_release_at = $1, paused = $2, version = version + 1
WHERE id = $3 AND deleted_at IS NULL;

-- Not a spec edit: neither the spec nor the version changes, so the app keeps
-- running. NULL clears it.
-- name: SetAppTrust :execrows
UPDATE apps SET trusted_until = $1 WHERE id = $2 AND deleted_at IS NULL;

-- Apps whose agent acts without asking, or did until a time now past.
-- name: ListTrustedApps :many
SELECT * FROM apps WHERE trusted_until IS NOT NULL AND deleted_at IS NULL ORDER BY id;

-- Ends an app's trust only if it is still the one read, so two gateway
-- replicas sweeping at once record one end.
-- name: EndAppTrust :execrows
UPDATE apps SET trusted_until = NULL WHERE id = $1 AND trusted_until = $2;
