-- +goose Up
-- A coding agent's sign-in that a person approves in the browser (RFC 8628
-- shape). code_hash is the SHA-256 of the device code only the agent holds;
-- user_code is what the person types or follows a link with. state moves
-- from pending to approved or denied, and from approved to spent when the
-- agent's poll takes the token; owner is the person who approved. The token
-- is made by that poll and never stored here. Times are Unix milliseconds;
-- last_poll_at is 0 until the first poll.
CREATE TABLE device_logins (
    id           TEXT PRIMARY KEY,
    user_code    TEXT NOT NULL UNIQUE,
    code_hash    BYTEA NOT NULL UNIQUE,
    created_at   BIGINT NOT NULL,
    expires_at   BIGINT NOT NULL,
    last_poll_at BIGINT NOT NULL,
    state        TEXT NOT NULL,
    owner        TEXT NOT NULL
);
CREATE INDEX device_logins_expires_at ON device_logins (expires_at);

-- +goose Down
DROP TABLE device_logins;
