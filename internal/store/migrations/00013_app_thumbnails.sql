-- +goose Up
-- The picture an app's owner chose for its card (config thumbnails.enabled).
-- data is a PNG or JPEG the gateway decoded and encoded again, so it holds
-- image data only; content_type is image/png or image/jpeg. digest is a
-- hash of data, put in the image's address so browsers never keep showing
-- an old picture. set_at is Unix milliseconds.
CREATE TABLE app_thumbnails (
    app_id       TEXT PRIMARY KEY REFERENCES apps (id) ON DELETE CASCADE,
    content_type TEXT NOT NULL,
    data         BYTEA NOT NULL,
    digest       TEXT NOT NULL,
    set_at       BIGINT NOT NULL
);

-- +goose Down
DROP TABLE app_thumbnails;
