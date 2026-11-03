-- +goose Up
-- Stop when idle: the app's timeout in minutes (0 is Off), and when traffic
-- last went through a gateway to it (Unix milliseconds; NULL before any).
-- Gateways only ever move last_active_at forward.
ALTER TABLE apps ADD COLUMN idle_stop_minutes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN last_active_at BIGINT;

-- +goose Down
ALTER TABLE apps DROP COLUMN last_active_at;
ALTER TABLE apps DROP COLUMN idle_stop_minutes;
