#!/bin/sh
set -eu
project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$project_dir"
exec docker compose --project-name atc-cf-bot \
  -f compose.yaml -f compose.vps.yaml "$@"
