# Go-Umstellung ab 0.9.0

Die separate Alpha-Phase ist beendet. `Dockerfile` und `compose.yaml` starten
jetzt den Go-Dienst; die frühere Python-Implementierung und ihre Runtime-
Abhängigkeiten wurden entfernt. Keine zweite Installation notwendig.

Auf dem bisherigen Docker-Host:

```bash
curl -fL https://raw.githubusercontent.com/quadrix-hd/orbit-central/main/scripts/pull-update.sh -o /tmp/orbit-pull-update.sh
bash /tmp/orbit-pull-update.sh /orbit-central
cd /orbit-central
docker compose exec orbit orbit version
docker compose ps
```

Der Updateweg sichert die Konfiguration, übernimmt das bisherige Datenvolume,
erhält CA-Mount und `.env` und ersetzt den vorhandenen Dienst durch Go.
Die frühere Go-Vorschau wird nach erfolgreichem Wechsel gestoppt; ihr separates
Volume bleibt erhalten. Verwende die bisherige Domain, nicht Port 8081.
Falls du an der Proxy-Konfiguration manuell Änderungen für 8081 vorgenommen
hast, stelle sie auf den bisherigen Hauptdienst zurück.

Alle zuvor im Python-Backend implementierten Bereiche sind nach Go portiert:
Authentifizierung, Verbindung/Failover, Monitoring, VM-/LXC-Grundaktionen,
QEMU-Hardware, Erstellung/Restore, HA, Blueprints/Klone, Aufgaben, Konsole,
Marketplace und Wartungswerkzeuge. Dies bedeutet keine vollständige Abdeckung
aller Proxmox-Funktionen. Bekannte Produktgrenzen und genaue Befehle stehen
in [README.md](../README.md); Testnachweise in [QA_REPORT.md](../QA_REPORT.md).

Der bestehende Python-Datenstand bleibt im Format SQLite Schema 1, Scrypt und
Fernet kompatibel. Nach einem erfolgreichen Update kann ein alter Rollback-
Image-Tag noch lokal vorhanden sein; es läuft dadurch kein zweiter Dienst.
