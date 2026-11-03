-- A taken name inserts nothing and returns no row.
-- name: InsertTemplate :one
INSERT INTO templates (name, source, spec, version, updated_at) VALUES ($1, $2, $3, 1, $4)
ON CONFLICT (name) DO NOTHING
RETURNING *;

-- Guarded by the version the caller read and by the source, so a chart sync
-- cannot take over an admin template. No match returns no row.
-- name: UpdateTemplate :one
UPDATE templates
SET spec = $1, version = version + 1, updated_at = $2
WHERE name = $3 AND version = $4 AND source = $5
RETURNING *;

-- name: GetTemplate :one
SELECT * FROM templates WHERE name = $1;

-- name: ListTemplates :many
SELECT * FROM templates ORDER BY name;

-- name: DeleteTemplate :execrows
DELETE FROM templates WHERE name = $1;
