#!/usr/bin/env bash
# Fetch an update without losing a local CA mount or changing .env.
set -euo pipefail
project_dir=${1:-/orbit-central}
cd "$project_dir"
[[ -f compose.yaml && -f scripts/update.sh ]] || { echo 'Kein Orbit-Projekt gefunden.' >&2; exit 1; }
command -v git >/dev/null || { echo 'Git fehlt.' >&2; exit 1; }
[[ $(git branch --show-current) == main ]] || { echo 'Update erwartet Branch main.' >&2; exit 1; }
if ! git diff --quiet HEAD -- . ':(exclude)compose.yaml'; then
  echo 'Weitere lokale Quellcodeänderungen gefunden. Update abgebrochen; bitte Änderungen zuerst prüfen.' >&2; exit 1
fi
git fetch origin main
previous_head=$(git rev-parse HEAD)
next_head=$(git rev-parse origin/main)
git merge-base --is-ancestor "$previous_head" "$next_head" || { echo 'Lokaler Verlauf weicht ab. Kein erzwungenes Zurücksetzen.' >&2; exit 1; }
backup_dir="backups/config-$(date +%Y%m%d-%H%M%S)-$$"
mkdir -p "$backup_dir"
chmod 700 "$backup_dir"
cp -p compose.yaml "$backup_dir/compose.yaml"
if [[ -f .env ]]; then cp -p .env "$backup_dir/.env"; chmod 600 "$backup_dir/.env"; fi
stage_dir=$(mktemp -d "$backup_dir/.merge-XXXXXX")
trap 'rm -rf "$stage_dir"' EXIT
git show "$previous_head:compose.yaml" > "$stage_dir/base.yaml"
git show "$next_head:compose.yaml" > "$stage_dir/incoming.yaml"
if ! git merge-file -p "$backup_dir/compose.yaml" "$stage_dir/base.yaml" "$stage_dir/incoming.yaml" > "$stage_dir/merged.yaml"; then
  echo "Konflikt in compose.yaml. Nichts ersetzt. Sicherung: $backup_dir" >&2; exit 1
fi
# Backed-up local Compose is restored if fast-forward cannot complete.
git restore --source=HEAD --staged --worktree -- compose.yaml
if ! git merge --ff-only origin/main; then
  cp -p "$backup_dir/compose.yaml" compose.yaml
  echo 'Quellcode-Update fehlgeschlagen; lokale Compose-Datei wiederhergestellt.' >&2; exit 1
fi
cp "$stage_dir/merged.yaml" compose.yaml
printf 'Konfiguration erhalten. Sicherung: %s\n' "$backup_dir"
bash scripts/update.sh
