-- +goose Up
-- The identity provider's roles (config rolesClaim) at the person's last
-- sign-in, a JSON list kept verbatim like groups. Templates and sizes can be
-- limited to them; scheduled runs read this snapshot.
ALTER TABLE people ADD COLUMN provider_roles TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE people DROP COLUMN provider_roles;
