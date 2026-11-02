#!/usr/bin/env bash
# Explicit offline restore; preserves current state before replacing it.
set -euo pipefail
cd "$(dirname "$0")/.."
[[ $# -eq 1 && -f "$1" ]] || { echo 'Aufruf: bash scripts/restore.sh backups/orbit-DATUM.tar.gz' >&2; exit 1; }
archive=$(realpath "$1")
docker compose stop orbit
# Save the pre-restore state in the volume; no running worker may mutate it.
docker compose run --rm -T --no-deps --entrypoint python orbit scripts/backup.py
# Stream the explicitly chosen backup into the stopped service volume.
cat "$archive" | docker compose run --rm -T --no-deps --entrypoint python orbit scripts/restore.py
docker compose up -d --no-build --wait --wait-timeout 120
printf 'Datenstand wiederhergestellt. Bestehende Proxmox-VMs wurden nicht verändert.\n'
