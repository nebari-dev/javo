-- +goose Up
-- The identity provider's refresh and id tokens of a session are now sealed
-- by the gateway's sessions key. Rows written before hold them in clear and
-- cannot be sealed here (the database has no key): those sessions end, and
-- their people sign in again. Their app sessions and tickets go first, by
-- name rather than by the cascade, so the result does not depend on the
-- engine enforcing foreign keys during a migration.
DELETE FROM sessions WHERE parent_id IN (SELECT id FROM sessions WHERE refresh <> '' OR id_token <> '');
DELETE FROM sessions WHERE refresh <> '' OR id_token <> '';

-- +goose Down
-- An older binary reads the tokens as they are stored, so sealed ones would
-- be sent to the provider as garbage: those sessions end too.
DELETE FROM sessions WHERE parent_id IN (SELECT id FROM sessions WHERE refresh <> '' OR id_token <> '');
DELETE FROM sessions WHERE refresh <> '' OR id_token <> '';
