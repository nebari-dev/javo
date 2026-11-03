#!/bin/sh
# Run a command with JAVO_TEST_PG_DSN pointing at a throwaway Postgres.
# JAVO_TEST_PG_CONTAINER names the container, so tests can run pg_dump and
# pg_restore from the same pinned image.
# The container (image pinned by digest) is removed when the command ends.
# Docker publishes it on a random loopback port; the password is random.
# Durability is off (fsync and friends): the data lives only for the tests.
#
# Usage: scripts/test/postgres-test.sh <command> [args...]
set -eu

image=postgres:17-alpine@sha256:18cfe3ef5e6815560c98237d6216d1e5119702fb0f3894c8785dd58b8bbe5d73
name=javo-test-postgres-$$
password=$(openssl rand -hex 24)

docker run -d --rm --name "$name" \
  -e POSTGRES_PASSWORD="$password" \
  -p 127.0.0.1::5432 \
  "$image" \
  -c fsync=off -c synchronous_commit=off -c full_page_writes=off >/dev/null
trap 'docker rm -f "$name" >/dev/null 2>&1 || true' EXIT

port=$(docker port "$name" 5432/tcp | head -n1 | sed 's/.*://')
i=0
until docker exec "$name" pg_isready -h 127.0.0.1 -U postgres >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -gt 60 ]; then
    echo "postgres did not become ready" >&2
    docker logs "$name" >&2 || true
    exit 1
  fi
  sleep 0.5
done
# pg_isready answers during the image's init-time restart; wait for the real server.
until docker exec "$name" psql -h 127.0.0.1 -U postgres -tAc 'select 1' >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -gt 120 ]; then
    echo "postgres did not accept connections" >&2
    exit 1
  fi
  sleep 0.5
done

JAVO_TEST_PG_CONTAINER="$name" JAVO_TEST_PG_DSN="postgres://postgres:${password}@127.0.0.1:${port}/postgres?sslmode=disable" "$@"
