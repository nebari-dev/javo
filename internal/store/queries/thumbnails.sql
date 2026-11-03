-- name: UpsertThumbnail :exec
INSERT INTO app_thumbnails (app_id, content_type, data, digest, set_at) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (app_id) DO UPDATE SET content_type = excluded.content_type, data = excluded.data,
    digest = excluded.digest, set_at = excluded.set_at;

-- name: GetThumbnail :one
SELECT content_type, data, digest FROM app_thumbnails WHERE app_id = $1;

-- name: GetThumbnailDigest :one
SELECT digest FROM app_thumbnails WHERE app_id = $1;

-- name: DeleteThumbnail :execrows
DELETE FROM app_thumbnails WHERE app_id = $1;
