-- name: InsertAudit :exec
INSERT INTO audit_events (id, at, actor, via, action, object_kind, object_name, object_id, object_namespace, object_owner, allowed, detail)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12);

-- name: ListAudit :many
SELECT * FROM audit_events ORDER BY at DESC, id DESC LIMIT $1;
