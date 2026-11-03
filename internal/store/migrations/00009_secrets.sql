-- +goose Up
-- People's secrets. ciphertext is the value sealed by package
-- secrets for this row (its id, owner and name) under the data keyset, as
-- base64; the clear value is never stored. version counts value changes:
-- apps that use the secret restart when it moves. owner is the person's
-- name, as on apps.
CREATE TABLE secrets (
    id TEXT PRIMARY KEY,
    owner TEXT NOT NULL,
    name TEXT NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    ciphertext TEXT NOT NULL,
    version BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    UNIQUE (owner, name)
);

-- Which secret an app gets as which environment variable. The store refuses
-- to delete a secret a live app uses; the cascades only clean up after apps
-- and secrets that are gone.
CREATE TABLE app_secrets (
    app_id TEXT NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    env TEXT NOT NULL,
    secret_id TEXT NOT NULL REFERENCES secrets (id) ON DELETE CASCADE,
    PRIMARY KEY (app_id, env)
);
CREATE INDEX app_secrets_secret ON app_secrets (secret_id);

-- The data keyset (one row, id 'data'): wrapped is its private half encrypted
-- by the key-encryption key, which is never in the database; public is the
-- half that seals and opens nothing. Both base64. version guards rotation.
CREATE TABLE secret_keysets (
    id TEXT PRIMARY KEY,
    wrapped TEXT NOT NULL,
    public TEXT NOT NULL,
    version BIGINT NOT NULL,
    updated_at BIGINT NOT NULL
);

-- +goose Down
DROP TABLE secret_keysets;
DROP INDEX app_secrets_secret;
DROP TABLE app_secrets;
DROP TABLE secrets;
