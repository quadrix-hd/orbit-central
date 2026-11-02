#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
command -v docker >/dev/null || { echo 'Docker Engine und Docker Compose werden benötigt.' >&2; exit 1; }
docker compose version >/dev/null
new_install=0
if [[ ! -f .env ]]; then cp .env.example .env; chmod 600 .env; new_install=1; fi
if [[ -t 0 && "$new_install" == 1 ]]; then
  printf '\nMarketplace: Wie sollen Paketdateien bereitgestellt werden?\n'
  printf '  1) Aus dem Web laden, wenn eine VM erstellt wird\n  2) Alle Dateien herunterladen und lokal speichern\n'
  read -r -p 'Auswahl [1]: ' catalog_choice
  case "$catalog_choice" in
    ''|1) catalog_mode=web ;;
    2) catalog_mode=cache ;;
    *) echo 'Ungültige Auswahl.' >&2; exit 1 ;;
  esac
  sed -i '/^ORBIT_CATALOG_MODE=/d' .env
  printf '\nORBIT_CATALOG_MODE=%s\n' "$catalog_mode" >> .env
fi
docker compose up -d --build --wait --wait-timeout 120
printf '\nOrbit läuft. Lokal öffnen: http://localhost:8080\n'
if [[ -t 1 ]]; then
  printf 'Einmaliger Einrichtungscode: '
  docker compose exec -T orbit cat /data/setup-token 2>/dev/null || printf 'Bereits eingerichtet.\n'
fi
printf '\nEinrichtungscode erneut lesen: docker compose exec orbit cat /data/setup-token\n'
printf 'Bei Remote-Installation SSH-Tunnel nutzen: ssh -L 8080:127.0.0.1:8080 BENUTZER@SERVER\n'
