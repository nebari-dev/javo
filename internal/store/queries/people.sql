-- name: GetPersonByName :one
SELECT * FROM people WHERE name = $1;

-- name: GetPerson :one
SELECT * FROM people WHERE id = $1;

-- name: ListPersonIDs :many
SELECT id FROM people ORDER BY id;

-- name: UIDTaken :one
SELECT COUNT(*) FROM people WHERE uid = $1;

-- name: GetPersonBySubject :one
SELECT * FROM people WHERE issuer = $1 AND subject = $2;

-- name: GetPersonByLinkedSubject :one
SELECT p.* FROM people p
JOIN person_subjects s ON s.person_id = p.id
WHERE s.issuer = $1 AND s.subject = $2;

-- name: ListLinkedSubjects :many
SELECT issuer, subject FROM person_subjects WHERE person_id = $1 ORDER BY issuer, subject;

-- A conflict (the name, the subject or the uid is taken) inserts nothing and
-- returns no row.
-- name: InsertPerson :one
INSERT INTO people (id, name, issuer, subject, username, email, display_name,
                    username_lower, email_lower, uid, groups, last_login_at, created_at,
                    provider_roles)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: UpdatePersonLogin :exec
UPDATE people
SET username = $1, email = $2, display_name = $3, username_lower = $4,
    email_lower = $5, groups = $6, last_login_at = $7, provider_roles = $8
WHERE id = $9;

-- $1 is the lower-cased prefix and $2 its length in characters.
-- name: SearchPeople :many
SELECT * FROM people
WHERE $1 = substr(username_lower, 1, $2) OR $1 = substr(email_lower, 1, $2)
ORDER BY name
LIMIT $3;

-- name: InsertPersonSubject :exec
INSERT INTO person_subjects (person_id, issuer, subject) VALUES ($1, $2, $3);

-- $1 is lower-cased: everyone whose username or email is exactly it.
-- name: ListPeopleByLogin :many
SELECT * FROM people WHERE username_lower = $1 OR email_lower = $1 ORDER BY name;

-- The provider said the person is gone: forget their groups and date their
-- last sign-in to the epoch, so their tokens pause until they sign in again.
-- name: RevokePersonLogin :execrows
UPDATE people SET groups = $1, provider_roles = $2, last_login_at = $3 WHERE name = $4;
