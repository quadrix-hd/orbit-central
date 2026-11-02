#!/usr/bin/env bash
# Neue, geprüfte Quelldateien zuerst in dieses Verzeichnis übernehmen.
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ -n "${ORBIT_IMAGE:-}" ]]; then echo 'ORBIT_IMAGE ist gesetzt. Image-Updates bitte explizit verwalten.' >&2; exit 1; fi
configured_image=$(docker compose config --images)
[[ "$configured_image" == 'orbit-console:local' ]] || { echo 'Update-Skript erwartet orbit-console:local. Für eigene Images den Updateweg explizit verwalten.' >&2; exit 1; }
mkdir -p backups
chmod 700 backups
container=$(docker compose ps -q orbit)
[[ -n "$container" ]] || { echo 'Orbit läuft nicht. Für Erstinstallation scripts/install.sh verwenden.' >&2; exit 1; }
old_image=$(docker inspect --format '{{.Image}}' "$container")
rollback_tag="orbit-console:rollback-$(date +%Y%m%d%H%M%S)"
docker image tag "$old_image" "$rollback_tag"
printf 'Neues Image bauen. Der bisherige Dienst bleibt währenddessen aktiv.\n'
docker compose build orbit
backup_name=''
# Prevent requests during health checks and database rollback.
docker compose exec -T orbit python -c 'from pathlib import Path;Path("/data/maintenance").touch(mode=0o600)'
restore_old() {
  trap - ERR INT TERM
  echo 'Update abgebrochen. Vorherige Version wird wiederhergestellt.' >&2
  docker compose stop orbit || true
  if [[ -n "$backup_name" ]]; then
    # Retain post-update state separately for diagnosis, without replacing the snapshot.
    docker compose run --rm -T --no-deps --entrypoint python orbit scripts/backup.py >&2 || true
    # Use the current package's validation helper in the old image, even when
    # upgrading from 0.3.0 (which did not contain restore.py).
    ORBIT_IMAGE="$rollback_tag" docker compose run --rm -T --no-deps \
      --volume "$PWD/scripts/restore.py:/tmp/orbit-restore.py:ro" \
      --entrypoint python orbit /tmp/orbit-restore.py "$backup_path" --keep-maintenance || { echo 'Restore fehlgeschlagen. Wartungsmodus bleibt aktiv; Backup nicht löschen.' >&2; exit 1; }
  fi
  docker image tag "$rollback_tag" orbit-console:local
  docker compose up -d --no-build --wait --wait-timeout 120 || { echo 'Vorherige Version konnte nicht starten. Wartungsmodus bleibt aktiv.' >&2; exit 1; }
  docker compose exec -T orbit python -c 'from pathlib import Path;Path("/data/maintenance").unlink(missing_ok=True)'
  echo "Vorherige Version wieder aktiv. Sicherung: ${backup_name:-nicht erstellt}" >&2
  exit 1
}
trap restore_old ERR INT TERM
docker compose stop orbit
# Use the old image to snapshot the old schema before starting new code.
backup_path=$(ORBIT_IMAGE="$rollback_tag" docker compose run --rm -T --no-deps --entrypoint python orbit scripts/backup.py)
backup_name=$(basename "$backup_path")
ORBIT_IMAGE="$rollback_tag" docker compose run --rm -T --no-deps --entrypoint python orbit -c \
  'import sys;from pathlib import Path;sys.stdout.buffer.write(Path(sys.argv[1]).read_bytes())' "$backup_path" > "backups/$backup_name"
chmod 600 "backups/$backup_name"
docker compose up -d --no-build --wait --wait-timeout 120
docker compose exec -T orbit python -c 'from pathlib import Path;Path("/data/maintenance").unlink(missing_ok=True)'
trap - ERR INT TERM
printf 'Update erfolgreich. Sicherung: backups/%s\n' "$backup_name"
