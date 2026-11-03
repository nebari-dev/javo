-- +goose Up
-- What the gateway looks for in an app's logs before they go to a model: per
-- secret, the length of its value and an HMAC-SHA256 of it under a key kept
-- outside the database (the controller writes them, as only it opens values).
-- key_id names that key and version is the secret's version the value had,
-- so a fingerprint of another key or of an older value is known to be stale.
-- An empty mac is a value too short to look for.
CREATE TABLE secret_fingerprints (
    secret_id TEXT PRIMARY KEY REFERENCES secrets (id) ON DELETE CASCADE,
    key_id TEXT NOT NULL,
    version BIGINT NOT NULL,
    length BIGINT NOT NULL,
    mac TEXT NOT NULL
);

-- +goose Down
DROP TABLE secret_fingerprints;
