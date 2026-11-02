#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p backups
chmod 700 backups
backup_path=$(docker compose exec -T orbit python scripts/backup.py)
docker compose cp "orbit:${backup_path}" backups/
chmod 600 "backups/$(basename "$backup_path")"
printf 'Backup: backups/%s\n' "$(basename "$backup_path")"
