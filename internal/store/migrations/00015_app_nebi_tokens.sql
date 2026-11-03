-- +goose Up
-- The Nebi token of an app's owner, for apps whose template asks for one
-- (spec.nebiToken) while the Nebi integration is on. The gateway trades the
-- owner's OIDC ID token for it and seals it with the public half of the
-- secrets keyset (internal/secrets), so the database never holds it in
-- clear; only the controller opens it, to copy it into the app's Secret.
-- expires_at and set_at are Unix milliseconds.
CREATE TABLE app_nebi_tokens (
    app_id     TEXT PRIMARY KEY REFERENCES apps (id) ON DELETE CASCADE,
    sealed     BYTEA NOT NULL,
    expires_at BIGINT NOT NULL,
    set_at     BIGINT NOT NULL
);

-- +goose Down
DROP TABLE app_nebi_tokens;
