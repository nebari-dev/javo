-- name: GetGroup :one
SELECT * FROM groups WHERE name = $1;

-- name: GIDTaken :one
SELECT COUNT(*) FROM groups WHERE gid = $1;

-- name: InsertGroup :execrows
INSERT INTO groups (name, gid) VALUES ($1, $2)
ON CONFLICT DO NOTHING;
