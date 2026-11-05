-- +goose Up
-- The AI settings a platform admin set in Settings > AI, used in place of
-- ai.provider, ai.baseURL, ai.model and the key of ai.apiKeySecret in the
-- platform config. One row or none; none means the config applies as it is.
-- key_sealed is the admin's key sealed by the gateway (package aisettings),
-- never in clear; key_last4 is what is shown of it. rev changes on every
-- write, so each gateway replica sees a change on its next model call.
CREATE TABLE ai_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    provider TEXT NOT NULL,
    base_url TEXT NOT NULL,
    model TEXT NOT NULL,
    key_sealed BYTEA,
    key_last4 TEXT NOT NULL DEFAULT '',
    rev TEXT NOT NULL,
    updated_at BIGINT NOT NULL
);

-- +goose Down
DROP TABLE ai_settings;
