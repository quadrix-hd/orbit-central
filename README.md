# Orbit Central · Go 0.11.0

Lokales Dashboard für Proxmox VE und optional Proxmox Backup Server.
Der Server, die Proxmox-Anbindung, Aufgabenverarbeitung, Konsole, Registry und
Wartungswerkzeuge laufen mit Go. Die Oberfläche verwendet HTML, CSS und
JavaScript. Der fertige Container enthält keinen Python-Dienst und benötigt
keinen Python-Interpreter. noVNC wird als statische Browserbibliothek
mitgeliefert; NoCloud-ISOs erstellt das lokale Werkzeug xorriso.

## Bestehende Installation auf Go umstellen

Auf dem Docker-Host:

```bash
curl -fL https://raw.githubusercontent.com/quadrix-hd/orbit-central/main/scripts/pull-update.sh -o /tmp/orbit-pull-update.sh
bash /tmp/orbit-pull-update.sh /orbit-central
```

Der Helfer lädt den aktuellen Stand, sichert die Konfiguration, erhält `.env`
und führt deine lokale `compose.yaml` mit der neuen Version zusammen. Bei
Konflikten oder weiteren lokalen Quellcodeänderungen bricht er ab. Änderungen
an der alten Vorschau-Datei `compose.go.yaml` sind erlaubt.

Danach baut er das Go-Image, setzt Wartungsmodus, stoppt den bisherigen Dienst,
sichert dessen Datenbank mit dem Go-Werkzeug und startet **denselben Dienst
`orbit` mit demselben Datenvolume und Port**. Bei einem fehlgeschlagenen Start
stellt er die Sicherung und das bisherige Image wieder her. Eine vorhandene
separate Go-Vorschau wird nach erfolgreichem Wechsel gestoppt; deren Volume
wird nicht gelöscht.

Bestehende Passwörter, Sitzungen, verschlüsselte API-Token, Verbindungen und
Aufgaben bleiben erhalten. Nach einem Restore werden Sitzungen verworfen.
Unklare externe Übermittlungen nach einem Neustart werden zur manuellen
Prüfung angehalten. Es werden keine Proxmox-Hosts oder VMs durch das Update
neu gestartet oder heruntergefahren. Bereits zuvor ausdrücklich eingeplante
Aufgaben können nach dem Start weiterbearbeitet werden.

Prüfen:

```bash
cd /orbit-central
docker compose exec orbit orbit version
docker compose ps
docker compose logs --tail=100 orbit
```

Erwartet: `0.9.0`, ein gesunder Orbit-Container. Die bisherige Domain und dein
Reverse-Proxy bleiben auf dem vorhandenen Orbit-Port. Keine Umleitung auf 8081
und keine erneute Einrichtung notwendig.

## Neu installieren

Docker Engine und Compose v2 erforderlich. Docker auf einer eigenen Linux-VM
betreiben, die alle Proxmox-Endpunkte erreichen kann. Mindestens 1 GiB RAM für
Docker; lokale Registry-Images benötigen zusätzlichen Plattenspeicher.

```bash
git clone https://github.com/quadrix-hd/orbit-central.git
cd orbit-central
bash scripts/install.sh
```

Beim ersten interaktiven Start wird gefragt, ob Marketplace-Dateien bei Bedarf
aus dem Web geladen oder vollständig lokal gespeichert werden sollen.

Für Zugriff aus dem LAN `.env` bearbeiten, beispielsweise:

```dotenv
ORBIT_LISTEN=0.0.0.0
ORBIT_PORT=8080
ORBIT_PUBLIC_ORIGIN=http://10.0.10.20:8080
```

Die Beispiel-IP durch die tatsächliche Docker-Host-IP ersetzen. Bei einem
HTTPS-Proxy `ORBIT_PUBLIC_ORIGIN=https://orbit.example.net` setzen. Der Ursprung
muss exakt zur Browseradresse passen. Danach:

```bash
docker compose up -d --build --wait
docker compose exec orbit cat /data/setup-token
```

Mit dem Einmalcode ein Administratorpasswort ab zwölf Zeichen festlegen.

## Proxmox verbinden

Unter Einstellungen eine HTTPS-Adresse wie `https://pve.example.net:8006`, den
Verbindungsnamen und einen Proxmox-API-Token im Format
`benutzer@realm!tokenname=geheimnis` hinterlegen. PDM ist nicht erforderlich.
Für Cluster mehrere Node-Adressen derselben Mitgliedschaft hinterlegen.
Lesezugriffe wechseln bei Nichterreichbarkeit den Endpunkt. Bereits gesendete
Schreibzugriffe werden nicht automatisch wiederholt.

Bei der Proxmox-eigenen CA die vertrauenswürdig vom Host übernommene
`pve-root-ca.pem` als `certs/proxmox-ca.pem` ablegen, die kommentierte
CA-Volumezeile in `compose.yaml` aktivieren und in `.env` setzen:

```dotenv
ORBIT_CA_FILE=/certs/proxmox-ca.pem
```

Danach `docker compose up -d --force-recreate`. Der verwendete Hostname bzw.
die IP muss auch zum Zertifikat passen. TLS-Verifikation wird nicht deaktiviert.

## Funktionsumfang

- Einrichtung, Anmeldung, Sitzungen, CSRF-/Ursprungsschutz, Anmeldebegrenzung.
- PVE-/PBS-Verbindungen, Inventar, Ressourcen und Modulübersichten.
- VM-/LXC-Details, Messwerte, Snapshots, Hardwareübersicht.
- VM-Start, Shutdown, Reboot, CPU/RAM, Backup, Migration und Snapshots.
- QEMU-Hardware: Laufwerk hinzufügen/vergrößern, NIC/VLAN, ISO, Metadaten.
- Bestätigtes VM-Löschen, Snapshot-Rollback und Snapshot-Löschen.
- Neue QEMU-VM aus ISO und Restore eines QEMU-Backups auf eine freie VMID.
- HA-Gastzustände bei vorhandenem Cluster-Quorum.
- Persistente, idempotente Aufgaben mit UPID-Verfolgung und manueller Prüfung.
- Vorlagen-Blueprints, vollständige Klone, Cloud-init-Konfiguration.
- QEMU-noVNC-Konsole mit einmaliger Freigabe, Sitzungsbindung und TLS-Proxy.
- Zentraler Marketplace mit Web-/Cache-Modus, SHA-256-Prüfung und periodischer
  Aktualisierung. Cloud-Image-Bereitstellung über Proxmox VE 9+.
- Go-Werkzeuge für Backup, Restore, Zugriffsreset und Update/Rollback.

Keine vollständige Abdeckung der Proxmox-Oberfläche: LXC-Erstellung und
LXC-Terminal, Host-Upgrades, umfassende Netzwerk-/Firewall-/Storage-Editoren,
mehrere Orbit-Benutzer mit Rollen und SSO sind nicht implementiert. Die
entsprechenden Module zeigen den beschriebenen Leseumfang. Keine Behauptung
vollständiger Nutanix-/Proxmox-Funktionsgleichheit.

## Marketplace

Standardquelle: https://quadrix-hd.github.io/orbit-central/catalog.json

Die Registry liefert versionierte Rezepte und Image-Prüfsummen. Große
Basisimages stammen von offiziellen Distributionsservern. Web-Modus: Proxmox
lädt das Image bei der Bereitstellung und prüft SHA-256. Cache-Modus: Orbit
hält die geprüften Images lokal vor und überträgt sie über die API. Eine
Katalogaktualisierung verändert keine bestehende VM.

Cloud-Image-Import benötigt VE 9+, einen ausdrücklich eingetragenen
HTTPS-Endpunkt des Ziel-Nodes und aktive Storages für `images`, `import` und
`iso`. Das Hinzufügen des Inhaltstyps `import` erfordert eine ausdrückliche
Storage-Bestätigung. Bereitstellungen starten den Gast nur bei ausgewählter
Startoption. Softwarepakete werden durch Cloud-init beim ersten Start
installiert und benötigen weiterhin Zugang zu den Paketquellen. Lokale
Imagehaltung ist keine vollständige Airgap-Paketquelle. Beim Debian-Latestimage
führt eine geänderte Upstream-Datei bis zur nächsten Registry-Veröffentlichung
zu einem Prüfsummenfehler statt zu einem ungeprüften Import.

## Sicherung und Wiederherstellung

```bash
bash scripts/backup.sh
bash scripts/restore.sh backups/orbit-DATUM.tar.gz
bash scripts/reset-access.sh
```

Backups enthalten eine konsistente SQLite-Kopie, den notwendigen Schlüssel,
gegebenenfalls den Einrichtungscode sowie Katalogmetadaten. Sie enthalten
keine Proxmox-VM-Backups und nicht den großen Image-Cache. Beide SQLite-Datei
und `master.key` werden zum Wiederherstellen benötigt. Archive geschützt
aufbewahren. Restore prüft Dateinamen, Größen, SQLite-Schema und verschlüsselte
Token vor dem Ersetzen. Restore und Zugriffsreset benötigen einen gestoppten
Dienst. Die Skripte erledigen dies; direkt gestartete Go-Dienste werden durch
eine Datensperre vor einem zweiten Go-Writer geschützt.

## Entwicklung

```bash
go test ./...
go vet ./...
CGO_ENABLED=0 go build -o orbit-go ./cmd/orbit
ORBIT_DATA_DIR=./data-go-preview ORBIT_PUBLIC_ORIGIN=http://localhost:8080 ./orbit-go
npm ci
npm test
go run ./cmd/orbit export-preview
```

Go 1.25+; Docker nutzt den festgelegten Toolchain-Stand. Für reale NoCloud-ISO-
Erstellung außerhalb des Containers muss xorriso installiert sein. Go-Tests
nutzen simulierte Proxmox-Antworten und lokale HTTPS-/WebSocket-Testserver.
Teststand und Grenzen: [QA_REPORT.md](QA_REPORT.md).

## Einstellungen ab 0.10

Die neue Einstellungsseite bietet drei Farbschemata (Cloud, Ocean, Dusk),
normal/groß gesetzte lokale Orbit-Sans-Schrift, kompakte/komfortable Tabellen
und eine automatische Aktualisierung mit 15/30/60/120 Sekunden oder manueller
Aktualisierung. Einstellungen werden im Go-Dienst gespeichert; vor dem Login
kann die zuletzt verwendete Darstellung aus dem Browser übernommen werden.

Weitere Bereiche: Standardressourcen für neue ISO-/Registry-VMs,
Verbindungen bearbeiten/entfernen, Marketplace-Modus und Katalogquelle,
Administratorpasswort ändern, andere Sitzungen abmelden, Systemstatus sowie
eine konsistente Workspace-Sicherung herunterladen. Die Sicherung enthält
vertrauliche Zugangsdaten und muss geschützt aufbewahrt werden.

Eine neue Katalogadresse wird gespeichert und nach `docker compose restart
orbit` aktiv. CA-Mount, öffentliche Adresse und Ports bleiben ausdrücklich
Deployment-Konfiguration in `.env`/`compose.yaml`; die UI zeigt deren Status.
Verbindungen mit offenen Aufgaben oder verknüpften Blueprints lassen sich
nicht entfernen. Beim Passwortwechsel werden andere Sitzungen verworfen.

## Live-Anzeige ab 0.11

Der Browser erhält authentifizierte Server-Sent Events über `/api/events`.
Orbit fragt die Proxmox-API ungefähr alle zwei Sekunden ab; deren eigene
Messwertrate, Antwortzeit und Netzwerklatenz begrenzen die Aktualität. Es ist
kein verzögerungsfreier Proxmox-Push. Inventar und Aufgaben werden laufend
übertragen; ausgewählte Module und VM-Details werden zusätzlich aktualisiert.
Netzwerk-/Firewall-Konfigurationsdaten werden im geöffneten Editorbereich etwa
alle vier Sekunden neu gelesen, solange kein Bestätigungsdialog offen ist.

Änderungen werden im bestehenden Dokument eingepflegt. Eingaben, Fokus und
Konsolen-DOM bleiben erhalten. Fehler markieren fehlende Verbindungen;
Sitzungsablauf schließt den Stream. Er verbindet sich automatisch neu. Bei
Verbindungsproblemen greift das eingestellte Fallback-Intervall. "Aus"
deaktiviert nur diesen Fallback, nicht den Live-Stream.

Reverse-Proxys müssen `/api/events` ungepuffert weiterreichen; Orbit setzt
`X-Accel-Buffering: no` und sendet alle zehn Sekunden Heartbeats. Ein sinnvoller
Proxy-Lese-Timeout liegt oberhalb dieses Intervalls. Es werden keine dauerhaften
Proxmox-Token an den Browser geschickt.

## Netzwerk und Firewall ab 0.11

Unter Netzwerk: Linux-Bridges, Bonds und VLANs anlegen/bearbeiten/entfernen,
physische Interfaces bearbeiten, IPv4/IPv6, Gateways, MTU, Autostart, Bridge-Ports,
VLAN-Fähigkeit, VLAN-Basisinterface und Bond-Modus einstellen. Speichern merkt
Änderungen auf Proxmox vor. Die API-Differenz wird angezeigt. Anwenden und
Verwerfen sind ausdrücklich bestätigte, separate Aktionen. Anwenden benötigt
ifupdown2 und kann den Managementzugriff beeinflussen. Physische Ports werden
hier nicht gelöscht; OVS-/SDN-Interfaces werden als lesend gekennzeichnet.

Unter Firewall: Datacenter-, Host- und QEMU-/LXC-Bereich auswählen; Regeln
anlegen, bearbeiten, löschen und umordnen; Aktivierung, Richtlinien und
bereichsspezifische Optionen bearbeiten. Unbelegte Richtlinien bleiben
standardmäßig geerbt. MAC-/NDP-Filter werden nicht beim bloßen Öffnen oder
Speichern stillschweigend deaktiviert. Spezial-/Gruppenregeln werden als
solche angezeigt und nicht in eine einfache Regel umgeschrieben.

Schreibaufträge sind persistent und idempotent. Konfigurationsrevisionen werden
beim Einplanen und unmittelbar vor dem Schreiben geprüft; Firewall-Änderungen
verwenden zusätzlich Proxmox-Digests, wo die API diese unterstützt. Die Host-
Netzwerk-API hat keine gleichwertige atomare Digest-Sperre: externe parallele
Änderungen lassen sich nicht vollständig ausschließen. Unklare Übermittlungen
werden nicht automatisch wiederholt. Bestehende Bestätigungsdialoge bleiben
an den ursprünglich gewählten Cluster und Node gebunden.

Coterm-Einordnung und vollständige offene Bereiche: [FEATURE_MATRIX.md](FEATURE_MATRIX.md).
