#!/usr/bin/env bash
# Python -> Go and Go -> Go, preserving the existing Compose volume and identity.
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ -n "${ORBIT_IMAGE:-}" ]]; then echo 'ORBIT_IMAGE ist gesetzt. Eigenes Image explizit verwalten.' >&2; exit 1; fi
[[ $(docker compose config --images) == orbit-console:local ]] || { echo 'Update erwartet orbit-console:local.' >&2; exit 1; }
mkdir -p backups
chmod 700 backups
container=$(docker compose ps -a -q orbit)
[[ -n "$container" ]] || { echo 'Kein bestehender Orbit-Container. scripts/install.sh verwenden.' >&2; exit 1; }
old_image=$(docker inspect --format '{{.Image}}' "$container")
rollback_tag="orbit-console:rollback-$(date +%Y%m%d%H%M%S)"
docker image tag "$old_image" "$rollback_tag"
printf 'Go-Image bauen. Der bisherige Dienst bleibt währenddessen aktiv.\n'
docker compose build orbit
backup_path=''
restore_old() {
  trap - ERR INT TERM
  echo 'Update fehlgeschlagen. Vorherige Version wird wiederhergestellt.' >&2
  docker compose stop orbit || true
  if [[ -n "$backup_path" ]]; then
    docker compose run --rm -T --no-deps --entrypoint orbit orbit restore "$backup_path" --keep-maintenance || { echo 'Restore fehlgeschlagen. Wartungsmodus bleibt aktiv; Backup behalten.' >&2; exit 1; }
  fi
  # Remove maintenance using the new Go tool before retagging the old image.
  docker compose run --rm -T --no-deps --entrypoint orbit orbit maintenance off || exit 1
  docker image tag "$rollback_tag" orbit-console:local
  docker compose up -d --no-build --wait --wait-timeout 120 || { echo 'Vorherige Version konnte nicht starten.' >&2; exit 1; }
  echo "Vorherige Version wieder aktiv. Sicherung: ${backup_path:-nicht erstellt}" >&2
  exit 1
}
trap restore_old ERR INT TERM
# The new CLI also operates on the old Python database format. No Python helper
# or Python runtime is required in the new image.
docker compose run --rm -T --no-deps --entrypoint orbit orbit maintenance on
docker compose stop orbit
backup_path=$(docker compose run --rm -T --no-deps --entrypoint orbit orbit backup)
backup_name=$(basename "$backup_path")
# Keep an additional host copy; the authoritative rollback copy remains in /data.
docker compose cp "orbit:${backup_path}" "backups/$backup_name"
chmod 600 "backups/$backup_name"
docker compose up -d --no-build --wait --wait-timeout 120
docker compose exec -T orbit orbit maintenance off
trap - ERR INT TERM
# Remove the former preview container only. Its separate data volume is retained.
preview_ids=$(docker ps -aq --filter label=com.docker.compose.project=orbit-go-preview --filter label=com.docker.compose.service=orbit-go)
for preview_id in $preview_ids; do
  docker stop "$preview_id"
  docker rm "$preview_id"
done
printf 'Go-Umstellung erfolgreich. Bisherige Adresse, Daten und CA bleiben erhalten. Sicherung: backups/%s\n' "$backup_name"
