-- A home that exists already inserts nothing.
-- name: InsertHome :execrows
INSERT INTO homes (id, namespace, owner, spec, status, version, created_at)
VALUES ($1, $2, $3, $4, '{}', 1, $5)
ON CONFLICT DO NOTHING;

-- name: GetHome :one
SELECT * FROM homes WHERE id = $1;

-- name: GetHomeByOwner :one
SELECT * FROM homes WHERE namespace = $1 AND owner = $2;

-- name: ListHomes :many
SELECT * FROM homes ORDER BY namespace, owner;

-- name: ListHomesByOwner :many
SELECT * FROM homes WHERE owner = $1 ORDER BY namespace;

-- name: UpdateHome :execrows
UPDATE homes SET spec = $1, status = $2, version = version + 1
WHERE id = $3 AND version = $4;

-- name: DeleteHome :execrows
DELETE FROM homes WHERE id = $1;

-- name: InsertSharedFolder :execrows
INSERT INTO shared_folders (id, namespace, name, group_name, spec, status, version, created_at)
VALUES ($1, $2, $3, $4, $5, '{}', 1, $6)
ON CONFLICT DO NOTHING;

-- name: GetSharedFolder :one
SELECT * FROM shared_folders WHERE id = $1;

-- name: GetSharedFolderByName :one
SELECT * FROM shared_folders WHERE namespace = $1 AND name = $2;

-- name: ListSharedFolders :many
SELECT * FROM shared_folders ORDER BY created_at, id;

-- name: UpdateSharedFolder :execrows
UPDATE shared_folders SET spec = $1, status = $2, version = version + 1
WHERE id = $3 AND version = $4;

-- name: DeleteSharedFolder :execrows
DELETE FROM shared_folders WHERE id = $1;

-- name: GetStorageSetting :one
SELECT * FROM storage_settings WHERE key = $1;

-- version 0 inserts; any other version updates only that version.
-- name: InsertStorageSetting :execrows
INSERT INTO storage_settings (key, value, version, updated_at) VALUES ($1, $2, 1, $3)
ON CONFLICT DO NOTHING;

-- name: UpdateStorageSetting :execrows
UPDATE storage_settings SET value = $1, version = version + 1, updated_at = $2
WHERE key = $3 AND version = $4;
