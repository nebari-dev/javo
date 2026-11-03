-- A new paste replaces the old one and raises the generation, so a hand-off
-- of the old paste cannot clear the new one. The row outlives a removal, so
-- the generation never goes back.
-- name: UpsertGitToken :exec
INSERT INTO app_git_tokens (app_id, token, generation, removed, set_at) VALUES ($1, $2, 1, FALSE, $3)
ON CONFLICT (app_id) DO UPDATE SET token = excluded.token, removed = FALSE,
    generation = app_git_tokens.generation + 1, set_at = excluded.set_at;

-- name: GetGitToken :one
SELECT token, generation, removed FROM app_git_tokens WHERE app_id = $1;

-- Keeps the row (and its generation) with no text, so a hand-off of an earlier
-- paste matches nothing.
-- name: RemoveGitToken :execrows
UPDATE app_git_tokens SET token = '', removed = TRUE, generation = generation + 1
WHERE app_id = $1 AND NOT removed;

-- Clears the text only if no newer paste arrived since the controller read it.
-- name: ClearGitToken :execrows
UPDATE app_git_tokens SET token = '' WHERE app_id = $1 AND generation = $2;
