# Orbit auf Go umstellen

Stand: 2. November 2026. Dies ist eine **Go-Testversion**, kein fertiger Ersatz
für den produktiven Python-Dienst. `Dockerfile` und `compose.yaml` bleiben
vorerst produktiv; `Dockerfile.golang` und `compose.go.yaml` bauen einen
eigenständigen Go-Dienst **ohne Python-Laufzeit**.

## Was bereits in Go läuft

| Bereich | Stand |
| --- | --- |
| Einrichtung, Passwortprüfung, Login/Logout | Implementiert; Tests für Python-Scrypt-Kompatibilität |
| Sitzungen, CSRF, Ursprungskontrolle, Anmeldebegrenzung | Implementiert; HTTP-Tests |
| SQLite-Schema und verschlüsselte API-Token | Kompatibel mit Schema 1 und Fernet; Tests mit Python-Testvektoren |
| Verbindungen anlegen und bearbeiten, PVE und PBS | Implementiert; Token werden nie an den Browser zurückgegeben |
| TLS mit eigener CA, Cluster-Lesefailover | Implementiert; keine automatischen Wiederholungen gesendeter Schreibzugriffe |
| Inventar, Templates, Nodes und Deployment-Ziele lesen | Implementiert |
| VM-Details, Hardwareansicht, Snapshots und Messwerte lesen | Implementiert; sensible Config-Felder werden gefiltert |
| Storage, Netzwerk, Sicherung/HA, Firewall/SDN, Updateübersicht | Lesende Schnittstellen implementiert |
| Bisherige Aufgaben, Blueprints und Audit lesen | Implementiert; vorhandene Aufgaben werden nicht erneut gestartet |
| VM-Aktionen, persistente Auftragsverarbeitung, manuelle Auftragsprüfung | **Noch zu portieren** |
| noVNC/WebSocket-Konsole | **Noch zu portieren** |
| Marketplace-Registry, Web/Cache-Modus und Bereitstellung | **Noch zu portieren** |
| Backup, Wiederherstellung, Passwortzurücksetzung und Update/Rollback | **Noch zu portieren** |

Nicht portierte Schreibfunktionen antworten ausdrücklich mit HTTP 501. Sie
werden weder als erfolgreich gemeldet noch in die Aufgabenwarteschlange
eingetragen. `/api/capabilities` zeigt den Umfang nach der Anmeldung an.
Eine dauerhaft sichtbare Meldung kennzeichnet die Go-Oberfläche als Testversion.

## Getrennt testen

Die Test-Compose-Datei verwendet einen eigenen Projektnamen, Container,
Port **8081** und Datenbestand. Sie übernimmt nicht automatisch bestehende
Verbindungen. Niemals Python und Go gleichzeitig gegen denselben Datenbestand
starten. Für einen Kompatibilitätstest ausschließlich eine konsistente
Sicherung in ein separates Datenvolume übernehmen; ein fertiger automatischer
Migrationsbefehl folgt erst mit Backup-/Restore-Parität.

Im Repository-Verzeichnis:

```bash
docker compose -f compose.go.yaml up -d --build
docker compose -f compose.go.yaml exec orbit-go cat /data/setup-token
docker compose -f compose.go.yaml logs --tail=100 orbit-go
```

Zugriff von einem anderen PC im LAN, beispielsweise mit Docker-Host
`10.0.10.20` (durch die tatsächliche Adresse ersetzen):

```bash
ORBIT_GO_LISTEN=0.0.0.0 \
ORBIT_GO_PUBLIC_ORIGIN=http://10.0.10.20:8081 \
docker compose -f compose.go.yaml up -d --build
```

Dann `http://10.0.10.20:8081` öffnen. Für den Betrieb hinter einem HTTPS-Proxy
`ORBIT_GO_PUBLIC_ORIGIN` auf dessen tatsächlichen Ursprung setzen. Die
Proxmox-CA-Zeile in `compose.go.yaml` bei Bedarf aktivieren und
`ORBIT_CA_FILE=/certs/proxmox-ca.pem` setzen, wie beim bisherigen Dienst.

Die Go-Vorschau stoppen, ohne ihre Daten zu löschen:

```bash
docker compose -f compose.go.yaml down
```

## Entwicklung

```bash
go test ./...
go vet ./...
CGO_ENABLED=0 go build -o orbit-go ./cmd/orbit
ORBIT_DATA_DIR=./data-go-preview ORBIT_PUBLIC_ORIGIN=http://localhost:8081 PORT=8081 ./orbit-go
```

Die Browseroberfläche bleibt HTML/CSS/JavaScript. Die Go-Implementierung liegt
unter `cmd/orbit` und `internal/orbit`. Für SQLite wird ein Go-Treiber verwendet;
ein C-Compiler ist im fertigen Runtime-Image nicht erforderlich.

Ein vollständiger Wechsel des Standardcontainers setzt die Portierung aller
oben offenen Bereiche, Upgrade-/Rollback-Tests und einen kontrollierten
Integrationstest gegen einen Proxmox-Testcluster voraus. Es wurden keine
produktiven VMs gestartet, gestoppt, neu gestartet oder verändert.

## Durchgeführte Prüfung

24 automatisierte Go-Tests bestehen. `go vet ./...` und ein Build mit
`CGO_ENABLED=0` sind erfolgreich; das gebaute Programm meldet
`0.8.0-go-alpha.1`. TLS wurde zusätzlich mit einem lokalen HTTPS-Testserver
geprüft: ein nicht vertrautes Zertifikat wird abgewiesen, die ausdrücklich
eingebundene Test-CA ermöglicht den Zugriff. Die übrigen Proxmox-Antworten
werden simuliert. Ein Docker-Build und ein Test an einem realen
Proxmox-/PBS-Cluster stehen aus, weil hier kein Docker-Daemon bzw. Testcluster
verfügbar ist. Der CI-Workflow führt den Docker-Build separat aus.
