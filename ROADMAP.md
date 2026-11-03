# Roadmap · Orbit Central bis 1.0

Ziel: ein eigenständiges Proxmox-Panel für den produktiven Einsatz in
Unternehmen, mit einem Nutanix-Prism-ähnlichen Aufbau und einem Funktionsumfang,
der PegaProx ebenbürtig ist. Grundlage dieser Reihenfolge sind die offenen
Bereiche aus [FEATURE_MATRIX.md](FEATURE_MATRIX.md),
[PEGAPROX_PARITY.md](PEGAPROX_PARITY.md) und
[ACCEPTANCE_MATRIX.md](ACCEPTANCE_MATRIX.md).

Die Reihenfolge folgt einer einfachen Regel: **Zuerst das, was Firmen am
Einsatz hindert, danach Komfort und Parität.** Ohne mehrere Benutzer, Rollen,
Alarme und nachvollziehbare Updates setzt kein Betrieb ein Panel produktiv ein,
egal wie viele Editoren vorhanden sind.

## 0.14 · Fundament für den Betrieb (in Arbeit)

- [x] Kompakteres Enterprise-Layout: niedrigere Kopfzeilen, neutrale
      Kennzahlkarten mit Auslastungsbalken, dichte Tabellen, sachliche Texte.
- [x] VM-Liste: sortierbare Spalten, Status-/Typfilter mit Zählern, Suche nach
      Name/VMID/Node, CPU-/RAM-Balken mit Schwellwerten, „Alle auswählen“.
- [x] Reverse-Proxy-fähig: `ORBIT_TRUSTED_PROXIES`, echte Client-IP für
      Anmeldebegrenzung, Audit und Zugriffsprotokoll.
- [x] HSTS, Permissions-Policy, COOP; fehlgeschlagene Anmeldungen im Audit.
- [ ] Gleiche Tabellen-Bausteine (Sortierung, Filter, Balken) für Hosts,
      Storage, Backups und Aufgaben.
- [ ] Frontend-Quellstruktur: lesbare Quelldateien (`web/`) mit
      reproduzierbarem Build nach `dist/`, CSS-Altschichten konsolidieren.

## 0.15 · Identität und Zugriff (Blocker für Firmen)

- Mehrere Orbit-Benutzer, Rollen (Administrator, Operator, Nur-Lesen) und
  Zuordnung zu Clustern/Pools.
- OIDC-Anmeldung (Entra ID, Keycloak, Authentik) und optional LDAP.
- TOTP-Zweitfaktor für lokale Konten, Sitzungsübersicht pro Benutzer.
- Audit mit Benutzer, Client-IP, Ziel und Ergebnis; Export als CSV/JSON.

## 0.16 · Überwachung und Alarme

- Metrik-Historie (RRD von Proxmox plus eigene Aufbewahrung) mit Diagrammen
  für Host, Gast und Storage.
- Alarmregeln (Host offline, Storage > 85 %, Backup fehlgeschlagen, Zertifikat
  läuft ab) mit Zuständen offen/bestätigt/gelöst.
- Benachrichtigung per E-Mail (SMTP), Webhook, Microsoft Teams/Slack.
- Ereignis-Zeitleiste pro Objekt (Nutanix-„Events & Alerts“).

## 0.17 · Datensicherung und Wiederherstellung

- Backup-Zeitpläne und Retention bearbeiten (vzdump-Jobs), PBS-Namespaces.
- Schutzrichtlinien: Gäste per Tag automatisch einem Zeitplan zuordnen.
- Replikations-Jobs anlegen/bearbeiten.
- Automatisierte Restore-Prüfung in isoliertem Netz mit Ergebnisbericht.

## 0.18 · Lifecycle und Wartung

- Wartungsmodus pro Host mit Evakuierung (Live-Migration aller Gäste).
- Rolling Updates: Host für Host evakuieren, aktualisieren, neu starten,
  zurückverteilen; jederzeit anhaltbar, mit Protokoll.
- Repositories, DNS/NTP und Dienste pro Host.
- Host-Shell über das vorhandene Konsolen-Gateway.

## 0.19 · Netzwerk, Sicherheit, Storage vervollständigen

- SDN (Zonen, VNets, Subnetze, EVPN/VXLAN), OVS.
- Firewall-Aliase, IPSets, Security Groups, Log-Ansicht.
- Ceph-Übersicht und Pools, Storage-Upload/-Löschen, Credentials.
- Vollständige QEMU-Geräteeditoren (EFI, TPM, PCI/USB-Passthrough, Cloud-init).

## 0.20 · Automatisierung und Platzierung

- Lastverteilung (DRS-ähnlich): Empfehlungen, optional automatisch, mit
  Affinitäts-/Anti-Affinitätsregeln.
- Kapazitätsplanung und Prognose („Runway“), Berichte als PDF/CSV.
- REST-API mit API-Tokens pro Benutzer und OpenAPI-Beschreibung.
- Cross-Cluster-Migration.

## 1.0 · Produktionsfreigabe

- Alle Abläufe der ACCEPTANCE_MATRIX an mindestens einer benannten
  Proxmox-VE-/PBS-Version real abgenommen.
- Last-, Ausfall- und Wiederherstellungstests dokumentiert.
- Signierte Container-Images, SBOM, Sicherheitsrichtlinie (`SECURITY.md`),
  Update-Kanal stabil/vorab.
- Betriebshandbuch: Installation, Härtung, Sicherung, Upgrade, Notfall.

## Bewusst nicht vor 1.0

XCP-ng/ESXi-Verwaltung, Plugin-Marktplatz für Dritte, Mandanten-Kundenportal,
Kosten-/Energieberichte. Diese Themen bauen auf Benutzer/Rollen, Alarmen und
der API aus 0.15–0.20 auf.
