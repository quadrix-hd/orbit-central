#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
[[ $# -eq 1 && -f "$1" ]] || { echo 'Aufruf: bash scripts/restore.sh backups/orbit-DATUM.tar.gz' >&2; exit 1; }
archive=$(realpath "$1")
docker compose exec -T orbit orbit maintenance on
docker compose stop orbit
docker compose run --rm -T --no-deps --entrypoint orbit orbit backup
cat "$archive" | docker compose run --rm -T --no-deps --entrypoint orbit orbit restore
docker compose up -d --no-build --wait --wait-timeout 120
printf 'Datenstand wiederhergestellt. Proxmox-VMs wurden nicht verändert.\n'
