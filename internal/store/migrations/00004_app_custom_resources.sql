-- +goose Up
-- A custom CPU and memory an app runs at instead of a named size (size is
-- then empty): millicores and bytes, both 0 for an app at a size.
ALTER TABLE apps ADD COLUMN cpu_millis BIGINT NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN memory_bytes BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE apps DROP COLUMN memory_bytes;
ALTER TABLE apps DROP COLUMN cpu_millis;
