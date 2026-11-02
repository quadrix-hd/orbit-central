# Orbit Go 0.9.0 · Prüfstand

Stand: 2. November 2026. Der Standardcontainer läuft mit Go. Die aktive
Python-Implementierung und Python-Wartungsprogramme sind aus dem Projekt
entfernt. Bestehende Daten bleiben im kompatiblen SQLite-/Scrypt-/Fernet-Format.

## Automatisierte Prüfungen

80 bestandene Go-Tests einschließlich Untertests. Keine übersprungenen
Testfälle im abschließenden Lauf (das CLI-Paket enthält keine separaten Tests).
Abgedeckt sind:

- Python-Fernet-Testvektor, Scrypt-Passwortkompatibilität, Schema-Ergänzungen,
  fehlender/falscher Schlüssel ohne stillen Ersatz.
- Einrichtung, Anmeldung, Cookies, Sitzungsablauf, Logout, CSRF, Ursprung,
  Anmeldebegrenzung, Größen- und Typprüfung, statische Dateipfade und HEAD.
- TLS mit lokalem HTTPS-Testserver: unbekannte CA abweisen, eigene CA vertrauen.
- Cluster-Lesefailover, bevorzugter Endpunkt, Clusteridentitätsprüfung,
  kein Wiederholen einer bereits gesendeten Schreibanfrage.
- VM-Aktionsdispatch, CPU/RAM, Snapshots, Backups, Migration, QEMU-Hardware,
  Metadaten, ISO, bestätigtes Löschen, HA und LXC-Grundaktionen.
- Identitätsänderung vor Dispatch, freie VMID beim Erstellen/Restore,
  Idempotenz und Reservierungen über beide Warteschlangen.
- Aufgabenfortsetzung per UPID ohne erneuten Dispatch; Crash-Markierungen
  werden zur manuellen Prüfung angehalten.
- Blueprint-Klonablauf ohne impliziten Gaststart.
- QEMU-Konsole mit lokalem HTTPS-/WebSocket-Testserver: binärer Datentransport,
  permanente Token nur serverseitig, Sitzungsende schließt die Verbindung.
- Konsistentes SQLite-Backup und Restore inklusive Schlüssel, Token und
  Passwort; Sitzungen werden verworfen. Unsichere Archive werden vor dem
  Ersetzen abgelehnt. Datensperre und Zugriffsreset.
- Marketplace-Web-/Cache-Modus, lokale Dateiprüfung, Ursprungskontrolle,
  Downloadfehler erhalten den vorherigen Katalog, Modus wird gespeichert.
- Erzeugte NoCloud-ISO mit xorriso; CIDATA, user-data, meta-data und
  network-config werden aus dem fertigen ISO gelesen/geprüft.
- Registry-Bereitstellungsablauf in beiden Modi gegen simulierte Proxmox-
  Antworten; kein Gaststart ohne ausdrückliche Auswahl.
- Update-Skript mit simuliertem Docker-CLI: Sicherung nach Dienststopp,
  Go-Werkzeuge statt Python und Restore vor Zurückschalten des Images.

Frontend-Prüfungen (`node tests/test_frontend.cjs`) bestehen: zwölf Ansichten,
Filter, Summen, HTML-Escaping, Konfigurator, Hardwaredialoge, LXC/HA,
Formularsperren, Authentifizierungsfehler, wiederverwendete Request-Keys und
nacheinander ausgeführte Aktualisierungen. Dies sind Rendering-/Verhaltenstests,
keine erneute visuelle Abnahme des Designs.

`go test -race ./...`, `go vet ./...`, Go-Build mit `CGO_ENABLED=0` und Shell-Syntaxprüfungen bestehen.
Der GitHub-Workflow führt zusätzlich einen Docker-Build und Container-Smoke-Test
sowie die Prüfung auf fehlenden Python-Interpreter im Runtime-Image aus.

## Grenzen der Abnahme

Hier steht kein Docker-Daemon und kein echter Proxmox-/PBS-Testcluster zur
Verfügung. Deshalb sind echte VM-Importe, Hardwareänderungen, Migrationen,
Backups, Wiederherstellungen und der reale Wechsel des Nutzercontainers nicht
hier ausgeführt worden. Es wurden keine produktiven VMs oder Hosts gestartet,
gestoppt, neu gestartet oder gelöscht. Der Update-/Rollback-Test simuliert
Docker-Aufrufe; der Datenbank-Restore selbst wird mit echter SQLite geprüft.

Keine vollständige Proxmox-Funktionsgleichheit: LXC-Terminal/-Erstellung,
Host-Upgrades, vollständige Netzwerk-/Storage-/Firewall-Editoren, mehrere
Orbit-Benutzer, RBAC und SSO sind weiterhin Produktlücken. Diese waren bereits
im bisherigen Backend nicht implementiert. Cloud-Image-Registry setzt VE 9+
und passende Storage-Inhaltstypen voraus. Lokaler Image-Cache ersetzt keine
Offline-Paketquelle für die Gastsoftware.
