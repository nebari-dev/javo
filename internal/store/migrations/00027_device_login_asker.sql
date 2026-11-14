-- +goose Up
-- What the approval page says about who asked: asked_from is the client
-- address the request was started from ('' when the gateway cannot tell
-- clients apart, behind a proxy it was not told to trust, and for requests
-- started before this column), client_name what the device called itself
-- ('' for none).
ALTER TABLE device_logins ADD COLUMN asked_from TEXT NOT NULL DEFAULT '';
ALTER TABLE device_logins ADD COLUMN client_name TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE device_logins DROP COLUMN client_name;
ALTER TABLE device_logins DROP COLUMN asked_from;
