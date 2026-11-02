#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
docker compose stop orbit
docker compose run --rm -T --no-deps --entrypoint orbit orbit reset-access
docker compose up -d --no-build --wait --wait-timeout 120
docker compose exec -T orbit cat /data/setup-token
