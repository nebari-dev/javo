-- +goose Up
-- The size a job's runs get (as apps.size): a size name, or a custom CPU and
-- memory in millicores and bytes (size then empty), both 0 for a job at a
-- size. An empty size on a job made before sizes means the default.
ALTER TABLE jobs ADD COLUMN size TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN cpu_millis BIGINT NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN memory_bytes BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE jobs DROP COLUMN memory_bytes;
ALTER TABLE jobs DROP COLUMN cpu_millis;
ALTER TABLE jobs DROP COLUMN size;
