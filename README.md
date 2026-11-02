# Orbit Central · 0.7.0

Lokale Verwaltungsoberfläche für Proxmox VE und Proxmox Backup Server. Die gehostete Website ist eine interaktive Demo. Der Docker-Dienst enthält API-Anbindung, SQLite, verschlüsselte Zugangsdaten und persistente Aufgaben.

Version 0.6.0 vereinheitlicht das Design, ergänzt mehrere VE-API-Endpunkte pro Cluster, eine LXC-Grundverwaltung und das Anlegen/Ändern von HA-Gastzuständen. Die direkte Verbindung zu Proxmox VE genügt; PDM wird nicht benötigt. Die Einrichtung zeigt den Einmalcode bei interaktivem Start direkt an.

**Status:** ausführbare Entwicklungsfassung für einen Administrator. Tests verwenden simulierte Proxmox-Antworten. Kein echter Proxmox-/PBS-Server und kein Docker-Daemon wurden hier zur Abnahme verwendet. Kein Anspruch vollständiger Funktionsgleichheit mit Prism, Nutanix Files, Objects, Flow, NKP oder DR.

## Starten

Voraussetzungen: Linux-VM mit Docker Engine und Docker Compose v2, mindestens 1 GiB RAM, Netzwerkzugriff auf VE/PBS. Eine separate Linux-VM ist dem Betrieb von Docker direkt auf dem Hypervisor vorzuziehen.

```bash
bash scripts/install.sh
```

Dann `http://localhost:8080` öffnen, den Einmalcode eingeben und ein Administratorpasswort mit mindestens 12 Zeichen setzen. Der Code wird nach der Einrichtung gelöscht.

Bei Installation auf einem anderen Rechner:

```bash
ssh -L 8080:127.0.0.1:8080 BENUTZER@SERVER
```

Im eigenen Browser `http://localhost:8080` öffnen. Standardmäßig bindet der Dienst nur an localhost. Für LAN-Nutzung einen HTTPS-Reverse-Proxy einrichten und `ORBIT_PUBLIC_ORIGIN=https://orbit.example.lan` in `.env` setzen. Keine Passwörter oder API-Tokens in die gehostete Demo eingeben.

## Einrichten

Unter **Einstellungen → Proxmox verbinden** den Typ wählen:

- **Proxmox VE:** z. B. `https://pve.example.lan:8006`
- **Proxmox Backup Server:** z. B. `https://pbs.example.lan:8007`

In Orbit ist das Eingabeformat für beide Token-Typen `Benutzer@Realm!Token-ID=Secret`. Der Dienst setzt den passenden Authorization-Header; für PBS wird der Secret-Trenner auf `:` umgestellt. API-Tokens lassen sich in einer bestehenden Verbindung ersetzen, ohne gespeicherte Geheimnisse wieder im Browser anzuzeigen.

Pro VE-Cluster genau eine Verbindung einrichten. Mehrere Nodes desselben Clusters nicht zusätzlich als eigene Verbindungen registrieren. Die Compute-Ansichten lassen sich auf eine VE-Verbindung einschränken; Betriebsansichten gruppieren Ergebnisse je Verbindung. PBS-Datastores stehen unter Datensicherung & HA.

### TLS und eigene CA

Die HTTPS-Zertifikatsprüfung bleibt aktiv. Die verwendete Adresse muss zum Zertifikat passen. Bei eigener CA:

1. CA-Zertifikat nach `certs/proxmox-ca.pem` legen. Mehrere vertrauenswürdige CAs können in einem PEM-Bundle stehen.
2. Zertifikats-Volume-Zeile in `compose.yaml` aktivieren.
3. In `.env` `ORBIT_CA_FILE=/certs/proxmox-ca.pem` setzen.
4. `docker compose up -d` ausführen.

API-Tokens sind mit Fernet verschlüsselt. Datenbank und Verschlüsselungsschlüssel liegen im geschützten Datenvolume. Ein Angreifer mit Zugriff auf das ganze Volume kann entschlüsseln; Backups enthalten deshalb sensible Daten. Keine Token oder Passwörter werden an die Demo übertragen.

### Rechte

Eigene Benutzer und getrennte API-Tokens verwenden. VE-Berechtigungen umfassen je nach verwendetem Funktionsbereich unter anderem:

- Lesen: `Sys.Audit`, `VM.Audit`, `Datastore.Audit`
- Bereitstellen/Konfigurieren: `VM.Clone`, `VM.Allocate`, `VM.Config.CPU`, `VM.Config.Memory`, `VM.Config.Disk`, `VM.Config.Network`, `VM.Config.Cloudinit`, `VM.Config.Options`, `Datastore.AllocateSpace`
- Verwaltung: `VM.PowerMgmt`, `VM.Console`, `VM.Snapshot`, `VM.Snapshot.Rollback`, `VM.Backup`, `VM.Migrate`, `VM.Allocate`

Die tatsächlichen Rechte und ACL-Pfade hängen von Proxmox-Version, SDN, Datastore und VM-Pool ab. Dies ist kein gegen reale Installationen geprüftes Minimalprofil. APT-, Firewall-, HA- und SDN-Endpunkte können zusätzliche Rechte benötigen. Ein Verbindungstest auf `/version` beweist nicht, dass alle Module erlaubt sind. Fehlende Rechte bleiben im jeweiligen Modul sichtbar; andere Ergebnisse werden weiter angezeigt.

Für PBS einen eigenen Benutzer und Token mit ausreichendem Datastore-Leserecht, z. B. `Datastore.Audit`, einrichten. PBS-Benutzer und Token benötigen passende ACLs. Diese Version liest PBS-Datastores und Aufgaben; sie ändert keine PBS-Retention-, Prune-, Verify- oder Sync-Jobs.

## Funktionsumfang

| Bereich | Diese Version |
|---|---|
| Dashboard | Aktuelle Node-/VM-Zahlen, CPU/RAM, Offline-Hinweise und Orbit-Aufgabenfehler |
| Multi-Cluster | Direkte VE-Verbindungen; aggregiertes und nach VE-Verbindung gefiltertes Inventar |
| VM-Details | Status, CPU/RAM, Laufwerksgrößen und Snapshot-Liste; sensible Konfigurationsfelder werden nicht ausgegeben |
| VM-Aktionen | Start, geordnetes Shutdown, Reboot, Snapshot ohne RAM, Rollback, Snapshot-Löschen, Backup, Restore in neue VMID, Migration und bestätigtes VM-Löschen |
| VM-Erstellung | Neue QEMU-VM aus vorhandenem Installations-ISO; keine bestehenden IDs überschreiben |
| Hardware | Disk hinzufügen/vergrößern, NIC/Bridge/VLAN, ISO/Bootmedium, Name, Notizen und Autostart bei gestoppter VM |
| Sammelaktionen | Start/Shutdown für bis zu 20 ausgewählte QEMU-VMs; Ergebnis je VM |
| Konsole | noVNC im Docker-Image, sitzungsgebundenes eigenes WebSocket-Gateway; reale VE-Kompatibilität noch offen |
| CPU/RAM | Änderung bei gestoppter VM, mit Konfigurations-Digest gegen konkurrierende Änderungen |
| Marketplace | Eigene Cloud-init-Blueprints, vollständiger Klon, Ressourcen, Netzwerk, SSH-Key und optionaler Start |
| Storage | VE-Storage-Konfiguration und Kapazität pro Node, lesend |
| Netzwerk | Interfaces und Bridges pro Node, lesend |
| Datensicherung/HA | Backup-Zeitpläne, Replikationsjobs und HA-Ressourcen, lesend; VM-Backups als Aktion |
| PBS | Datastore-Kapazitäten und Aufgaben, lesend |
| Firewall/SDN | Cluster-Firewalloptionen, Regeln und VNets, lesend |
| Lifecycle | Installierte Versionen und verfügbare Updates aus dem APT-Cache, lesend |
| Aufgaben | Persistente Bereitstellungen und Verwaltungsaktionen mit Protokoll und Wiederaufnahme |

LXC wird im Inventar angezeigt. Die neuen Verwaltungsaktionen betreffen QEMU-VMs. Ein Gast-Dateibrowser, vollständige LXC-Verwaltung, Host-Shell, Rollen/SSO und vollständige Host-/Storage-Konfiguration sind weiterhin nicht implementiert.

## Marketplace vorbereiten

Eine QEMU-Linux-VM mit Cloud-init-Laufwerk und maximal einer Netzwerkkarte vorbereiten, gewünschte Software installieren und als Vorlage konvertieren. Das Systemlaufwerk muss `scsiN`, `virtioN` oder `sataN` heißen. Für Docker, PostgreSQL oder Nginx jeweils eine vorbereitete Vorlage verwenden.

Im Blueprint Vorlage, Node, Storage, Netzwerk-Bridge, Cloud-init-Benutzer und Ressourcen festlegen. Orbit prüft Vorlagenstatus, Cloud-init, NIC-Anzahl und Disk-Größe beim Veröffentlichen, beim Absenden und erneut vor dem Klonen. Die VM bleibt auf dem Node der Vorlage und erhält IPv4-DHCP an der gewählten Bridge.

Die Demo-Einträge werden nicht automatisch in den lokalen Live-Katalog übernommen. Es gibt keinen automatischen Image-Download und keine nachträgliche Installation beliebiger Software. Mehrere Netzwerkkarten, Windows/Sysprep, statische IP-Pools und Multi-VM-Blueprints sind nicht umgesetzt.

## Verwaltungsaktionen

VM-Namen öffnen die eigene Objektseite mit Übersicht, Hardware, Netzwerk, Snapshots, Backup/Restore, Konsole und Optionen. Jede Aktion verlangt die Bestätigung des VM-Namens. Snapshots benötigen passenden Storage-Support; Backups brauchen aktives Backup-Storage auf dem Quell-Node. Migrationen erfolgen innerhalb desselben Clusters zu einem anderen Online-Node, ohne Force-Modus. Lokale Laufwerke, PCI-Geräte oder Netzwerk-/Storage-Konfigurationen können eine Migration verhindern.

Konfigurationsänderungen sind auf gestoppte VMs begrenzt. Es gibt kein erzwungenes Ausschalten. VM-Löschen erfordert zusätzlich die VMID und eine Datenverlustbestätigung. Restore verwendet ausschließlich eine neue, freie VMID. Shutdown und Reboot können Dienste unterbrechen und hängen von der Unterstützung im Gast ab.

Der Dienst speichert die Aktion vor der Übermittlung. Bis zu drei Verwaltungsaktionen können unabhängig von der Marketplace-Bereitstellung verarbeitet werden. Pro VM blockiert eine laufende oder unklare Verwaltungsaktion weitere Aktionen. Doppelte Anfrage-IDs liefern dieselbe Aufgabe zurück.

Beim Neustart werden bekannte UPIDs weiter beobachtet. Ein Abbruch zwischen Übermittlung und Speichern der UPID führt zu **Prüfung nötig**; es gibt keine automatische erneute Ausführung. Ein Proxmox-Task, der nach 30 Minuten noch aktiv ist, benötigt ebenfalls Prüfung. Die Aufgabe oder VM wird nicht automatisch gelöscht.

Nach Kontrolle in Proxmox lässt sich im Aufgabenprotokoll die Prüfung dokumentieren. VM-Name, eine nachvollziehbare Notiz und ausdrückliche Bestätigung sind erforderlich. Dadurch wird nur die lokale Aktionssperre freigegeben; es wird kein Erfolg auf Proxmox vorgetäuscht und keine entfernte Aktion ausgeführt.

Ein erfolgreicher Proxmox-Start beweist keine Bereitschaft der Gast-Anwendung. Cloud-init, SSH und Dateisystemvergrößerung müssen in der Testvorlage geprüft werden.

## Eingebettete Konsole

Das Docker-Image installiert noVNC aus dem Debian-Repository und `websocket-client` über die gepinnten Python-Anforderungen. Für manuelle Entwicklung muss `ORBIT_NOVNC_ROOT` auf ein noVNC-Verzeichnis mit `core/rfb.js` zeigen. Ohne diese Abhängigkeiten gibt der Dienst einen expliziten Fehler zurück.

Die Oberfläche lädt noVNC lokal, fordert eine kurzlebige Konsolenfreigabe an und verbindet sich mit dem eigenen Gateway. Der dauerhafte API-Token bleibt im Backend. Die Freigabe ist an die Orbit-Sitzung gebunden, nach 60 Sekunden ohne Verwendung ungültig und nur einmal verwendbar. Origin- und Sessionprüfung bleiben aktiv; Logout oder Sitzungsablauf beendet die Verbindung. Maximal vier aktive Konsolen, maximal 30 Minuten Laufzeit. Eingaben können den Gast verändern; „Strg+Alt+Entf senden“ ist eine bewusste Aktion.

Benötigt `VM.Console`. Je nach VE-Version kann die VNC-WebSocket-Authentifizierung mit API-Token abgelehnt werden. Orbit deaktiviert dann weder TLS noch andere Schutzmechanismen und zeigt einen Fehler. **Eine echte VE-Konsole wurde hier nicht abgenommen.** Gateway-Handshake und Binärübertragung wurden mit einem kontrollierten Stub getestet; das ersetzt keinen noVNC-/Proxmox-End-to-End-Test.

Bei HTTPS-Reverse-Proxy WebSocket-Upgrades weiterleiten, z. B. bei nginx:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_read_timeout 1900s;
}
```

`ORBIT_PUBLIC_ORIGIN` muss exakt der Browser-Adresse entsprechen. Der Ausschnitt ersetzt keine vollständige TLS-/Proxy-Konfiguration.

## ISO, Hardware und Recovery

Neue VMs wählen einen Online-Node, Image-Storage, eine Bridge, Ressourcen und optional ein bereits vorhandenes ISO. Ein Bootmedium wird nicht automatisch heruntergeladen. Der OS-Installer wird anschließend über die Konsole bedient. Die VM startet nicht automatisch.

Disk-Vergrößerung erfolgt auf absolute Größe; Verkleinerung wird verweigert. Eine neue Disk nutzt einen freien SCSI-Anschluss. NIC-Änderungen erhalten eine vorhandene MAC-Adresse und bestehende zusätzliche NIC-Parameter. ISO-Wechsel ersetzt weder eine Cloud-init-Disk noch ein Datenlaufwerk an `ide2`.

Rollback verwirft Änderungen nach dem gewählten Snapshot. Bei einem RAM-Snapshot kann Proxmox die VM automatisch starten. Backup-Restore legt eine neue VM an und setzt kein Force-Flag. Netzwerkidentität und IP-Konflikte im wiederhergestellten Gast müssen vor dem Start kontrolliert werden.

## Noch offene Produktbereiche

Die folgenden Bereiche sind weiterhin offen oder nur lesend: Orbit-Mehrbenutzerrollen/SSO, LXC-Schreibverwaltung, Host-Shell, vollständiges VM-Boot-/UEFI-/PCI-Passthrough-Management, Host-Netzwerk- und Storage-Schreibverwaltung, Firewall-Regeländerungen, HA-Policy-Bearbeitung und Paketinstallation. 0.6.0 ist deshalb noch kein vollständiger Ersatz der gesamten Proxmox-WebUI.

## Ökosystem: Abgrenzung zu Nutanix

Die Seite **Integrationen** zeigt denselben Umfang wie diese Dokumentation. Insbesondere:

- **Files / Objects / externe Volumes:** keine automatisierte NAS-, S3- oder SAN-Provisionierung. Benötigt eigene Anbieter-/Lifecycle-Adapter.
- **Flow:** die vorhandene Firewall-Übersicht ist keine Microsegmentierungsplattform und verändert keine Regeln.
- **NKP:** ein Kubernetes-Node-Blueprint ist kein vollständiges Cluster-, Upgrade- oder Policy-Management.
- **DR:** Replikation und HA werden angezeigt; Recovery-Pläne, Failover/Failback, RPO/RTO-Überwachung und Abnahmetests fehlen.
- **Firmware/Hypervisor-Upgrades:** Lifecycle installiert nichts. Hardware-Kompatibilität, Wartungsmodus, Drain/Migration, Upgrade und Rollback sind noch kein implementierter Ablauf.
- **PDM:** direkte VE-Multi-Cluster-Anbindung ist vorhanden, ein eigener Datacenter-Manager-Connector nicht.

Diese Bereiche sind sichtbare offene Integrationen, keine durch Menüeinträge behaupteten fertigen Funktionen.

## Updates

Neue geprüfte Quelldateien in dasselbe Projektverzeichnis übernehmen; `.env` und Volume behalten:

```bash
bash scripts/update.sh
```

Das Skript baut das neue Image, behält das alte Image als Rückweg, setzt einen Wartungsmarker, stoppt Orbit, sichert SQLite samt Schlüssel und startet die neue Version mit Healthcheck. Bei erkanntem Fehler wird der gesicherte Datenstand mit dem alten Image wiederhergestellt. Ein kurzes Wartungsfenster entsteht. Eigene Image-Konfigurationen werden nicht still überschrieben.

0.6.0 ergänzt eine Operationstabelle und den Verbindungstyp. Vorhandene Verbindungen werden als VE übernommen. Die Datenbankänderung ist additiv; ein Downgrade sollte trotzdem nur über den zum alten Image gehörenden Backup-Stand erfolgen. Das Skript führt diesen Restore beim Rollback aus.

Kein Docker-Daemon war für echte Container-Tests verfügbar. Die Update-Erfolgs-/Fehlerpfade sind mit einem Befehlsstub geprüft. Vor produktiver Nutzung einmal auf einem separaten Docker-Testhost durchspielen. Der lokale Healthcheck beweist keine funktionierende VE-/PBS-Verbindung.

## Backup und Restore

```bash
bash scripts/backup.sh
bash scripts/restore.sh backups/orbit-DATUM-ZUFALL.tar.gz
```

Backups umfassen SQLite und `master.key`, ggf. den Einrichtungscode. Die SQLite-Backup-API liefert einen konsistenten Datenstand. Der Restore stoppt Orbit, sichert den bisherigen Stand und validiert Archivmitglieder, Datenbankintegrität und Schlüssel, bevor er die Dateien ersetzt. Alte Sitzungen werden verworfen. Nur eigene vertrauenswürdige Archive verwenden.

Orbit-Backups sichern keine Proxmox-VMs und machen keine entfernten Verwaltungsaktionen rückgängig. `.env`, Compose-Anpassungen und CA-Zertifikate separat sichern. `docker compose down -v` würde das Datenvolume löschen.

## Passwort zurücksetzen

Mit administrativem Zugriff auf den Docker-Host und bei gestopptem Dienst:

```bash
docker compose stop orbit
docker compose run --rm -T --no-deps --entrypoint python orbit scripts/reset-access.py
docker compose up -d --no-build --wait
docker compose exec orbit cat /data/setup-token
```

Danach ein neues Passwort festlegen. Proxmox-Verbindungen und VMs werden nicht gelöscht.

## Entwicklung und Prüfungen

```bash
python -m venv .venv
. .venv/bin/activate
pip install -r requirements.txt
python -m unittest discover -s tests -v
node tests/test_frontend.cjs
node --check dist/app.js
node --check dist/vm-workspace.js
node --check dist/bootstrap.js
ORBIT_DATA_DIR=./data ORBIT_PUBLIC_ORIGIN=http://localhost:8080 python backend/server.py
```

Keine Frontend-Abhängigkeiten und kein Build notwendig. Ein Image enthält Oberfläche, API, Bereitstellungsworker und Verwaltungsworker. Details zu Testergebnissen und Grenzen stehen in `QA_REPORT.md`.

## Weiterer Ausbau

`PRODUCT_SCOPE.md` hält das Zielbild, den aktuellen Stand und die nächsten Integrationen fest. PegaProx wird als ergänzende Funktionsreferenz genutzt; die Implementierung bleibt eigenständig.

## Offizielle Referenzen

- Nutanix Prism als Produkt-/Bedienreferenz: https://www.nutanix.com/products/prism
- Proxmox VE API: https://pve.proxmox.com/pve-docs/api-viewer/
- Proxmox PBS API: https://pbs.proxmox.com/docs/api-viewer/index.html
- PBS-Token-Authentifizierung: https://pbs.proxmox.com/docs/user-management.html
- VE-Updates: https://github.com/proxmox/pve-docs/blob/master/system-software-updates.adoc

Orbit ist ein eigenes Projekt und kein offizielles Produkt von Nutanix oder Proxmox.


## Cluster-Ausfälle

Weitere VE-Adressen lassen sich an einer bestehenden Verbindung speichern. Lesen wechselt bei Ausfällen auf einen anderen geprüften Cluster-Endpunkt; Schreibaktionen werden nach unklarem Versand nicht automatisch wiederholt. Für den Orbit-Dienst selbst empfiehlt sich eine eigene Proxmox-HA-VM. Sie hat bei Hostausfall eine Wiederanlaufzeit, keine durchgängige Erreichbarkeit. Details, Voraussetzungen und Testplan: [HIGH_AVAILABILITY.md](HIGH_AVAILABILITY.md).

## Funktionsgrenzen

Ein vollständiger Ersatz aller Proxmox-WebUI-Funktionen ist weiterhin nicht erreicht. Insbesondere fehlen LXC-Erstellung/Terminal/Mountpoints/Restore, Host-Shell, RBAC/SSO, vollständige schreibende Storage-/Netzwerk-/Firewall-/Ceph-Konfiguration, HA-Gruppen/-Regeln, Rolling Upgrades, ISO-Uploads sowie DR-Orchestrierung. HA-Zielzustände sind neu schreibend angebunden; die übrigen Cluster-Betriebsübersichten bleiben lesend.


## Neu in 0.7.0

Kompakter Arbeitsbereich mit Funktionsleiste und Cluster-/Node-/Gastbaum. Fehlerantworten enthalten eine Fehler-ID; Logs protokollieren Statuscode und Laufzeit ohne Request-Bodies, Tokens oder Query-Parameter. HEAD-Abfragen werden unterstützt. Proxmox-Fehler unterscheiden TLS-Probleme und Verbindungsfehler und nennen bei verfügbaren Validierungsantworten Feldnamen und erforderliche Privilegien, ohne rohe Antworten auszugeben.

Der zentrale Marketplace hat einen lokalen, integritätsgeprüften Paketspiegel mit Start-/Update-Synchronisierung. Die tatsächliche Herausgeber-URL und das Bereitstellungsformat fehlen noch; automatische VM-Imports daraus sind noch nicht angeschlossen. [MARKETPLACE_SOURCE.md](MARKETPLACE_SOURCE.md) beschreibt den Transport und die Grenzen.

## Update von einer bestehenden GitHub-Installation

Der Update-Helfer erhält `.env` und führt lokale Änderungen an `compose.yaml` (z. B. den CA-Mount) mit der neuen Quelldatei zusammen. Vorher wird eine geschützte Konfigurationssicherung unter `backups/config-*` angelegt. Bei Konflikten oder weiteren lokalen Quellcodeänderungen bricht er ab. Anschließend nutzt er den vorhandenen Updateweg mit Datenbanksicherung und Image-Rollback.

Für eine Installation unter `/orbit-central`, als root:

```bash
curl -fL https://raw.githubusercontent.com/quadrix-hd/orbit-central/main/scripts/pull-update.sh -o /tmp/orbit-pull-update.sh
bash /tmp/orbit-pull-update.sh /orbit-central
```

Dabei wird nur Orbit für das Update kurz gestoppt und neu gestartet. Es werden keine Proxmox-Gäste neu gestartet. Die realen Docker-Abläufe benötigen weiterhin Abnahme; der neue Git-Helfer wurde mit echten lokalen Git-Repositories und einem simulierten Update-Aufruf geprüft.

## Zentrale Registry-Webseite

Der Ordner `docs/` enthält die Registry-Seite, `catalog.json` und sechs lokale Cloud-init-Rezeptdateien mit Größen und SHA-256-Prüfsummen. GitHub Pages kann den Ordner von `main` veröffentlichen. Die Rezepte sind keine VM-Abbilder und wurden nicht gegen reale Gäste abgenommen. Automatisches VM-Provisioning daraus bleibt offen.
