-- A row kept for another workload is left as it is: no row is written.
-- name: UpsertBundle :execrows
INSERT INTO workload_bundles (namespace, name, workload_id, owner, meta, sealed, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (namespace, name) DO UPDATE SET owner = excluded.owner, meta = excluded.meta,
    sealed = excluded.sealed, updated_at = excluded.updated_at
WHERE workload_bundles.workload_id = excluded.workload_id;

-- name: GetBundle :one
SELECT workload_id, owner, meta, sealed FROM workload_bundles WHERE namespace = $1 AND name = $2;

-- name: DeleteBundle :exec
DELETE FROM workload_bundles WHERE namespace = $1 AND name = $2 AND workload_id = $3;

-- Every bundle, for rotation.
-- name: ListSealedBundles :many
SELECT namespace, name, workload_id, sealed FROM workload_bundles ORDER BY namespace, name;

-- The same values sealed again (rotation): a bundle replaced since it was
-- read is left alone.
-- name: ResealBundle :execrows
UPDATE workload_bundles SET sealed = $1 WHERE namespace = $2 AND name = $3 AND sealed = $4;
