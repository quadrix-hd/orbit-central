# Funktions- und Abnahmestand · Orbit Go 0.13

Stand: 3. November 2026. Ziel ist ein eigenständiges Proxmox-Panel mit
Nutanix-orientiertem Aufbau und vergleichbaren Verwaltungsabläufen. Es besteht
**keine vollständige Funktionsgleichheit** mit Nutanix oder PegaProx.

## Was als fertig gelten darf

Ein Menüpunkt, ein API-Handler oder ein bestandener Unit-Test genügt nicht.
Für jede Funktion werden dokumentiert:

1. Ein konkret unterstützter Ablauf einschließlich Rechte und Voraussetzungen.
2. Backend- und UI-Implementierung mit echten Fehlerzuständen.
3. Automatisierte Prüfungen für Erfolg, Ablehnung, Konflikte und Wiederaufnahme.
4. Abnahme an einer benannten Proxmox-Version mit passendem Storage/Gasttyp.
5. Bei Ausfall-/Wiederherstellungsfunktionen: ein reproduzierbarer Ausfalltest.

`Implementiert / lokal geprüft` bezeichnet Code und lokale automatisierte Tests.
`Realabnahme offen` bedeutet, dass noch kein entsprechender Test an einem echten
Proxmox-System dokumentiert ist. Für die in dieser Sitzung ergänzten Funktionen
wurden keine produktiven Hosts oder Gäste verändert.

## Prism-orientierte Anforderungen

Referenz: https://www.nutanix.com/library/datasheets/nutanix-prism
(öffentliches Herstellerdatenblatt, geprüft am 3. November 2026).
Die Tabelle ordnet dessen Funktionsbereiche möglichen Proxmox-Entsprechungen zu;
sie ist keine vollständige Spezifikation aller Nutanix-Produkte und Editionen.

| ID | Zielbereich | Orbit / Proxmox-Entsprechung | Stand und Abnahmebedarf |
| --- | --- | --- | --- |
| N01 | VM-Lebenszyklus | QEMU-Erstellung, Ressourcen, Migration, Snapshots, Backup/Restore | Teilweise implementiert; weitere Devices, Policies, Realabnahme offen |
| N02 | Storage-Verwaltung | PVE-Storage-Konfiguration und Gastvolumes | Teilweise; Ceph-/Pool- und weitere Backend-Verwaltung offen |
| N03 | Netze und Isolation | Linux-Bridges/Bonds/VLAN, künftig SDN/EVPN | Linux-Grundfunktionen implementiert; Overlay-/Mandantenabläufe offen |
| N04 | Sicherheitsrichtlinien | PVE-Firewall statt Nutanix Flow | Regeln/Optionen implementiert; Richtlinienmodell, Spezialobjekte, Realabnahme offen |
| N05 | Schutz und Wiederherstellung | PVE/PBS, Replikation und Recovery-Pläne | Einzelgastabläufe vorhanden; Zeitpläne, DR-Orchestrierung und Ausfalltests offen |
| N06 | Identität und Projekte | Benutzer/Rollen, SSO, Pools, Quoten | Weitgehend offen; aktuell ein Administrator |
| N07 | Lifecycle | Geprüfte Proxmox-/Host-Updates und Wartungsabläufe | Paketübersicht vorhanden; Orchestrierung offen |
| N08 | Management-Verfügbarkeit | API-Fallback und eigene verfügbare Orbit-Instanzen | API-Fallback vorhanden; Orbit-HA-Koordinierung und Ausfallabnahme offen |
| N09 | Zustand und Diagnose | Live-Messwerte, Events, Audit, Support-Bundle | Messwerte/Aufgaben vorhanden; Healthchecks, Alarme und Diagnoseabläufe offen |
| N10 | Richtlinien und Automatisierung | Tags, Regeln, Scheduler, Platzierung | Tags und Einzelaktionen vorhanden; Regel-/Planungs-Engine offen |
| N11 | Nutanix-Plattformdienste | Eigene Integrationen für vergleichbare Dienste | AOS/AHV/Flow sind keine enthaltenen Proxmox-Dienste. Files/Objects/Kubernetes/Hersteller-Lifecycle erfordern zusätzliche Backends und eigene Abnahme. |

## PegaProx-Abgleich

Die konkreten offenen Bereiche stehen in [PEGAPROX_PARITY.md](PEGAPROX_PARITY.md).
Öffentliche Referenzen: https://docs.pegaprox.com/ und
https://github.com/PegaProx/project-pegaprox . Ein vollständiger Vergleich muss
gegen eine festgelegte Version und deren tatsächlich unterstützte Abläufe
geführt werden; neue Releases ändern den Umfang.

## In Go 0.13 ergänzte Abläufe

| ID | Implementierung | Automatisierte Prüfung | Realabnahme |
| --- | --- | --- | --- |
| CT01 | Neue CTID, vorhandenes vztmpl, rootdir-Storage, MiB-RAM/Swap, SSH-Schlüssel, DHCP/VLAN, Berechtigungsmodus | Erfolg, belegte ID, ungültige Quelle/Schlüssel, Bestätigung, keine automatischen Starts, unklare Übermittlung | Offen |
| CT02 | LXC-Datei-/PBS-Backup in neue CTID, ohne Überschreiben, neue MAC, gestoppt | Archivtyp, Task-Status, erneute Vorprüfung und keine Wiederholung | Offen; UID-/Mountpoint-/Storage-Besonderheiten je Backup prüfen |
| CT03 | Laufende LXC-Konsole über Proxmox VNC-Terminal und Go-WebSocket-Gateway | TLS-Testserver, korrekter Gastpfad, Passwort/Ticket, Binärtransport, Abmeldung, gestoppter Gast | Offen; Proxmox-Version, Tokenrechte und Proxy prüfen |
| CT04 | Nesting/Keyctl/FUSE bearbeiten, andere Feature-Flags erhalten | Zusammenführung mehrerer Flags und vorhandener Optionen | Offen; Berechtigungen und Gastkompatibilität prüfen |

Alle neuen Schreibabläufe bleiben ausdrücklich bestätigungspflichtig im Panel.
Ein erzeugter oder wiederhergestellter Container wird nicht automatisch gestartet.
Die Konsole startet einen gestoppten Container ebenfalls nicht.
