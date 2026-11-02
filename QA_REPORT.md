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

## Erweiterung 0.10.0

87 Go-Tests und Untertests bestehen einschließlich Race-Prüfung. Neue Tests
prüfen dauerhafte Darstellungseinstellungen und Grenzen, Passwortwechsel
(auch mit Leerzeichen im bisherigen Passwort), Sitzungswiderruf, validierte
Katalogquelle mit Aktivierung beim Neustart, Verbindungsentfernung ohne
Proxmox-Schreibzugriff und geschützten Sicherungsdownload als gültiges Archiv.
Frontend-Prüfungen umfassen sechs neue Einstellungsbereiche, Ressourcenwerte
und HTML-Escaping der Systeminformationen.

Die Gestaltung nutzt lokale Orbit-Sans-Schriften, größere Standardtexte,
Indigo/Lavendel bzw. Petrol/Mint sowie ein optionales dunkles Farbschema.
Die lokale Preview-Infrastruktur ist nicht verfügbar; die statische Demo
wird für die visuelle Kontrolle veröffentlicht. Reale Proxmox-Systeme werden
dabei nicht angesprochen.

Die veröffentlichte 0.10-Demo wurde im Cloud-Browser auf dem Desktop visuell
geprüft: Dashboard und neue Darstellungseinstellungen; Cloud/Ocean/Dusk wurden
über die echten Formulare ausgewählt und gespeichert. Der lokale Schriftwechsel
und die Farbschemata sind sichtbar. Zwei beobachtete Kontrastfehler im
Navigationskopf und beim dunklen Button-Hover wurden korrigiert. Die GitHub-
Prüfung einschließlich Docker-Build und Runtime-Smoke-Test ist erfolgreich.
Eine mobile Browser-Abnahme und die echte Kundeninstanz stehen weiterhin aus.

Zusätzlich prüft ein weiterer Go-Test in 0.10 den statischen Export und den automatischen Wechsel der Asset-Version bei geänderten Styles. Insgesamt bestehen damit 88 Tests und Untertests; Race-Prüfung und Frontend-Prüfungen bestehen ebenfalls.

## Live-Anzeige und Infrastruktur · 0.11.0

Zusätzliche Go-Tests verwenden einen echten lokalen SSE-HTTP-Client: Anmeldung,
Ursprungsschutz, Session-Widerruf, Stream-Abbruch und Freigabe des Clientlimits.
Ein absichtlich auf 50 Millisekunden begrenzter normaler HTTP-Write-Timeout
wird für den Stream korrekt überschrieben; mehrere Snapshots kommen weiter an.

Netzwerk-/Firewalltests prüfen CRUD-Dispatch, explizite Netzwerkaktivierung,
Konfigurationsrevisionen vor dem Schreiben, Digest-Übermittlung, Zielbestätigung,
Adressvalidierung, gesperrtes Löschen physischer NICs sowie das Nichtwiederholen
unklarer Übermittlungen. Bereichsspezifische Firewall-Optionen und das Bewahren
geerbter Richtlinien werden zusätzlich geprüft.

DOM-Tests mit jsdom prüfen erhaltene Eingaben/Fokus, den unveränderten App-Root
und Konsolenknoten, aktualisierte Kennzahlen/Node-Selektoren, passende Firewall-
Felder je Bereich und unveränderliche Editor-Ziele samt Request-Key bei einer
zwischenzeitlich geänderten Clusterliste. jsdom und Node sind ausschließlich
Entwicklungs-/Testwerkzeuge; die Runtime bleibt Go.

Es wurden keine echten Host-Netzwerke oder Firewalls geändert. Die Proxmox-
Schreibzugriffe werden simuliert. Die aktuelle Feature-Matrix bezeichnet offene
OVS-/SDN-, IPSet-/Alias-/Security-Group- und weitere Editorbereiche ausdrücklich.
Coterm wurde anhand seines offiziellen Quellcodes geprüft, nicht im Netzwerk
des Nutzers getestet oder umkonfiguriert.

Abschließender lokaler Lauf: 120 Go-Tests und Untertests einschließlich Race-Prüfung bestehen; Go-Vet, CGO-freier Build und beide Frontend-/DOM-Prüfungen sind erfolgreich.

## Go 0.12 · direkte Lastmessung, Storage und Gastkonfiguration

- 152 Go-Tests einschließlich Untertests mit `go test -race ./...` bestanden.
- Direkte `/nodes/{node}/status`, `/qemu?full=1` und `/lxc`-Werte unterscheiden sich
  im Test absichtlich vom alten Cluster-Cache. Die SSE-Messwerte erreichen den
  Client im Sekundentakt, obwohl APT-/Moduldaten blockiert sind.
- Zwanzig gleichzeitige Messwertabonnements teilen eine Runde. Nach Ablauf der
  kurzen Cache-Zeit wird erneut gelesen. Es gibt keine Proxmox-Schreibabfragen
  im Messwertpfad.
- QEMU-/LXC-Ressourcen mit MiB-RAM und Swap, dezimaler Übertragung großer Werte,
  CPU-Limits, Netzwerkänderungen mit erhaltenen MAC-/Modell-/Spezialoptionen,
  Agentoptionen sowie Disk-/Mountpoint-Erstellung, Resize und Storage-Wechsel
  gegen Fake-Proxmox getestet. Keine automatischen Lifecycle-Aufrufe.
- Storage-Teilspeicherung, Anlegen, Aufheben einer Node-Beschränkung, redigierte
  Antworten und sortierte Volume-Listen geprüft. Stale Digests und fremde/feste
  Felder verhindern Schreibzugriffe; unklare Übermittlungen werden nicht wiederholt.
- `npm test`: alle bisherigen Render-/DOM-Prüfungen plus Schutz gegen alte
  Inventar-/Detailantworten, feste Storage-Reihenfolge bei vertauschter API-Antwort,
  erhaltene Formulare und QEMU-/LXC-Ressourcen-/Netzwerk-/Disk-Editoren bestanden.
- Go-Build ohne CGO und `go vet ./...` lokal geprüft.

Die Tests ersetzen keine reale Proxmox-Abnahme: insbesondere Storage-Rechte,
Backend-Kompatibilität, Hotplug/Pending und die Weiterleitung von SSE durch den
Reverse-Proxy sind installationsabhängig. Es wurden keine echten Hosts/Gäste
verändert oder neu gestartet. Der PegaProx-Abgleich steht in PEGAPROX_PARITY.md;
volle Funktionsgleichheit wird ausdrücklich nicht als abgeschlossen ausgewiesen.
