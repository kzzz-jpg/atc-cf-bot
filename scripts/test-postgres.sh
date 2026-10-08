#!/bin/sh
set -eu
name="atc-cf-test-$$"
cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM
docker run -d --name "$name" -e POSTGRES_PASSWORD=test_only -e POSTGRES_DB=atc_test -p 127.0.0.1::5432 postgres:18.0-bookworm >/dev/null
count=0
until docker exec "$name" pg_isready -U postgres -d atc_test >/dev/null 2>&1; do
  count=$((count+1))
  if [ "$count" -gt 30 ]; then echo 'PostgreSQL did not become ready' >&2; exit 1; fi
  sleep 1
done
port=$(docker port "$name" 5432/tcp | sed 's/.*://')
TEST_DATABASE_URL="postgres://postgres:test_only@127.0.0.1:$port/atc_test?sslmode=disable" go test -race -count=1 ./internal/store ./internal/posting ./internal/scheduler
