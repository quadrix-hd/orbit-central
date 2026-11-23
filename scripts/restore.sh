#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

check_only=false
if [[ ${1:-} == --check ]]; then
  check_only=true
  shift
fi
[[ $# -eq 1 && -f "$1" ]] || { echo 'Aufruf: bash scripts/restore.sh [--check] backups/orbit-DATUM.tar.gz' >&2; exit 1; }
archive=$(realpath "$1")

# Use the running version's full restore validation in a disposable directory.
docker compose exec -T orbit sh -ec '
  stage=$(mktemp -d "${ORBIT_DATA_DIR:-/data}/.restore-check-XXXXXX")
  trap '\''rm -rf -- "$stage"'\'' EXIT
  ORBIT_DATA_DIR="$stage" orbit restore
' < "$archive"
echo 'Archivprüfung erfolgreich.'
if "$check_only"; then
  exit 0
fi

docker compose exec -T orbit orbit maintenance on
docker compose stop orbit
if ! docker compose run --rm -T --no-deps --entrypoint orbit orbit backup; then
  echo 'Sicherung fehlgeschlagen. Orbit wird mit dem bisherigen Datenstand gestartet.' >&2
  docker compose up -d --no-build --wait --wait-timeout 120 orbit
  docker compose exec -T orbit orbit maintenance off
  exit 1
fi
docker compose run --rm -T --no-deps --entrypoint orbit orbit restore < "$archive"
docker compose up -d --no-build --wait --wait-timeout 120 orbit
printf 'Datenstand wiederhergestellt. Proxmox-VMs wurden nicht verändert.\n'
