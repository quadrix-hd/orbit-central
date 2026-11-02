# Orbit Central 0.6.0 – Prüfbericht

Stand: 2. November 2026.

## Umgesetzt und geprüft

- 93 Python-Tests: HTTP-Setup, Auth/CSRF, Token-Verschlüsselung, Bereitstellungen, Verwaltungsaktionen, Hardware/Erstellung/Recovery, API-Transport, Restore, Update-Steuerung und Konsolengateway.
- Frontend-Prüfungen: zwölf Verwaltungsansichten plus VM-Objektseite mit sieben Reitern, Hardwareformulare, Erstellungsdialog, Filter, Escaping, Labels/Patterns, Sitzungsablauf, Dialogsperren und stabile Anfrage-IDs.
- Python-/JavaScript-/Shell-Syntax und Whitespace-Check.
- Echter lokaler Programmeinstieg ohne Projekt-PYTHONPATH: Healthcheck 0.6.0, sieben Frontend-Assets einschließlich beider Schriftdateien und sauberer Shutdown. Keine entfernte Infrastruktur kontaktiert.

## Neue Nachweise

- Disk-Erstellung wartet auf die asynchrone Konfigurationsaufgabe.
- Disk-Verkleinerung wird verweigert; vorhandene NIC-MAC und zusätzliche Parameter bleiben erhalten.
- ISO-Wechsel überschreibt kein Cloud-init-Laufwerk.
- Erstellung und Restore verwenden freie VMIDs und reservieren sie lokal; Restore setzt kein Force-Flag.
- Snapshot-Rollback und -Löschen benötigen explizite Bestätigung.
- VM-Löschen verlangt Namen, VMID, gestoppten Zustand und Datenverlustbestätigung.
- Konsolenfreigaben sind kurzlebig, einmalig und an Sitzung/Origin gebunden.
- WebSocket-Handshake und Binärübertragung wurden gegen einen kontrollierten Upstream-Stub geprüft.
- Fehlerhafte WebSocket-Frames, ungültige Upgrade-Schlüssel und fehlende noVNC-Abhängigkeiten werden abgewiesen.

## Aussagegrenzen

Es wurde kein echter Proxmox-/PBS-Server angesprochen. Entfernte API-Antworten in den Tests sind simuliert. Es gibt hier keinen Docker-Daemon; noVNC-Installation, Image-Build und Containerbetrieb sind deshalb nicht abgenommen. Die lokale Python-Umgebung hat `websocket-client` nicht installiert; die Gateway-Tests verwenden dafür einen expliziten Stub.

**Kein bestandener echter noVNC-End-to-End-Test:** Proxmox-Versionen können die WebSocket-Anmeldung mit API-Token unterschiedlich behandeln. Diese Integration muss mit der Zielversion, ihrem Token und ihrer CA geprüft werden. Erfolg des isolierten Relays beweist nicht die Kompatibilität mit Proxmox.

Browser-Layoutprüfung bleibt offen, weil die lokale Vorschau-Infrastruktur fehlt und die private Site eine Anmeldung verlangt. Nach der früheren automatischen Ablehnung wurde kein erneuter Anmeldeversuch durchgeführt.

Noch kein vollständiger Proxmox-WebUI-Ersatz: Host-Shell, vollständige LXC-Verwaltung, Rollen/SSO, schreibende Cluster-Netzwerk-/Storage-/Firewall-Verwaltung und vollständige HA-Gruppen-/Regelverwaltung, Rolling Upgrades und weitergehende Nutanix-Dienste sind nicht implementiert. Die Darstellung darf keine vollständige Produktparität behaupten.

## Erforderliche Testhost-Abnahme

1. Neues Docker-Image bauen und bestehendes 0.4.0-Volume aktualisieren.
2. CA und Tokenrechte einschließlich VM.Console prüfen; WebSocket-Reverse-Proxy testen.
3. Neue Test-VM aus ISO erstellen, noVNC verbinden und OS installieren.
4. Gestoppte VM: Disk hinzufügen/vergrößern, NIC/VLAN, ISO, Name und Autostart ändern.
5. Snapshot erstellen, Teständerung vornehmen und Rollback prüfen.
6. Backup erzeugen und in neue VMID wiederherstellen; IP-/MAC-Konflikte vor dem Start berücksichtigen.
7. Nur eine entbehrliche Test-VM mit vollständiger Bestätigung löschen.
8. Sammelaktion mit Teilfehler und Orbit-Neustart während einer bekannten Aufgabe prüfen.
9. Backup/Restore des Orbit-Dienstes und Update-Rollback mit realem Docker abnehmen.

Diese Abnahme steht aus. Die Tests liefern keine Garantie für störungsfreien Betrieb oder Datensicherheit in einer realen Infrastruktur.


## Änderungen in 0.6.0

- API-Endpunkte: Prüfung von HTTPS-Adressen, Clustername und Node-Mitgliedschaft; automatischer Lese-Failover mit bevorzugtem erfolgreichen Endpunkt. Kein Wiederholen bereits versendeter Schreibaktionen. Tests für Ausfall, Berechtigungsfehler, Cluster-Verwechslung und unklare Schreibantwort.
- Konsolen behalten den API-Endpunkt der Ticket-Erstellung. Keine behauptete transparente Wiederverbindung laufender Konsolen.
- LXC: Grundverwaltung verwendet `/lxc/` und CT-Hostnamen; keine QEMU-Socket-Konfiguration. Tests für Start, Ressourcen, Datenfilterung und Ablehnung nicht unterstützter Hardwareaktionen und Online-Migration.
- HA-Gastzustände: Anlegen/Ändern einer HA-Ressource mit expliziter Zustandsauswahl; Quorum-Prüfung bei Einplanung und Ausführung. Tests für CT-Ressourcen-ID, Quorumverlust und ungültige Zustände. Keine vollständige Prüfung von Fencing/Storage.
- Alte, blasse Workspace-Designschicht entfernt; gemeinsames Design für Navigation, Tabellen, Karten, Dialoge und Objektseiten. Layout weiterhin nicht im echten Browser abgenommen: `sites-previewd` steht nicht zur Verfügung. PxlFun-Referenz wurde im Browser visuell betrachtet; das ist kein Orbit-Layouttest.
- Installationsskript zeigt bei interaktivem Start den Einmalcode an.

Hochverfügbarkeit des Orbit-Dienstes ist nicht als aktive/aktive Funktion implementiert. Der dokumentierte Weg ist eine vom vorhandenen Proxmox-HA-System neu gestartete VM; reale Ausfalltests stehen aus. Siehe HIGH_AVAILABILITY.md.

- Noto Sans Regular/Bold werden als lokale WOFF-Dateien ausgeliefert; keine externe Font-Abhängigkeit. Lizenz liegt in FONT_LICENSE.txt.


## 0.7.0 – neuer Arbeitsbereich, Diagnose und Katalogtransport

Die angehängten Kundenlogs enthalten 464 Zeilen, überwiegend Request-URLs ohne Antwortstatus. Daraus konnten keine konkreten Fehlfunktionen bewiesen oder als behoben markiert werden. Es wurden keine Kunden-VMs angesprochen, gestartet, gestoppt oder neu gestartet. Der Browser-Zugriff auf die Kundeninstanz bleibt auf Wunsch aus.

Neue Prüfungen: 9 Katalogtests (Offline-Nutzung, Hashfehler, Größen-/Origin-Prüfung, Parallelität, Cache-Reparatur) und 4 HTTP-Diagnosetests (Status/Fehler-ID, HEAD, Zugriffsschutz). Frontend-Prüfungen decken Ressourcenbaum und Escaping zentraler Katalogeinträge ab. Der komplette Lauf umfasst 106 Python-Tests.

Offen: tatsächlicher Herausgeber-Katalog, automatische Proxmox-Bereitstellung zentraler Pakete, reale Proxmox-Fehler-Reproduktion und Browser-Layoutprüfung. Ein lokaler Paketcache allein ist kein abgeschlossener VM-Marketplace. Der Hintergrund-Sync wird bei Dienststart ausgelöst; laufende Gäste werden dadurch nicht aktualisiert.

## Git-Updatehelfer und Registry-Seite

Drei zusätzliche Prüfungen mit echten lokalen Git-Repositories: lokale CA-Einbindung und `.env` bleiben erhalten; Compose-Konflikte und weitere lokale Quellcodeänderungen brechen ohne Überschreiben ab. Der innere Docker-Update-Aufruf ist dabei ein Stub. Die sechs Rezeptdateien des Registry-Katalogs wurden auf Schema, Größe und SHA-256 geprüft. Die Rezepte selbst sind noch nicht auf Gastmaschinen abgenommen.
