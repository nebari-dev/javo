-- +goose Up
-- Facts about the database itself (for example whether the one-time import of
-- custom resources finished). Application data gets its own tables.
CREATE TABLE meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at BIGINT NOT NULL
);

-- +goose Down
DROP TABLE meta;
