-- +goose Up
-- A person's own model for their coding-agent apps, used while the admin
-- has none: an OpenAI-compatible base URL and model. The key is the person's
-- secret named JAVO_AGENT_KEY (sealed like any secret); key_last4 is what is
-- shown of it.
CREATE TABLE agent_settings (
    owner TEXT PRIMARY KEY,
    base_url TEXT NOT NULL,
    model TEXT NOT NULL,
    key_last4 TEXT NOT NULL DEFAULT '',
    updated_at BIGINT NOT NULL
);

-- +goose Down
DROP TABLE agent_settings;
