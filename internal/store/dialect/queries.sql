-- name: InsertThing :one
INSERT INTO things (id, name, flag, count, doc, note, created_at, seen_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: UpsertThing :one
INSERT INTO things (id, name, flag, count, doc, note, created_at, seen_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (id) DO UPDATE SET name = excluded.name, count = things.count + excluded.count, seen_at = excluded.seen_at
RETURNING *;

-- name: ThingsByNameOrNote :many
SELECT * FROM things WHERE name = $1 OR note = $1 ORDER BY id;

-- name: ThingsWithNamePrefix :many
SELECT * FROM things WHERE name LIKE CAST($1 AS TEXT) || '%' ORDER BY id;

-- name: CountThingsFlagged :one
SELECT COUNT(*) FROM things WHERE flag = $1;
