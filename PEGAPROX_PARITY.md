# PegaProx als Funktionsreferenz · Orbit Go 0.13

Ziel: vergleichbarer Verwaltungsumfang mit eigener Go-Implementierung und einer
Nutanix-orientierten Navigation. Keine Zusage, dass der aktuelle Stand bereits
alle PegaProx-Funktionen enthält. Es wurde kein PegaProx-Quellcode übernommen.

Referenzen, geprüft am 2. November 2026:
- https://docs.pegaprox.com/getting-started/overview/
- https://docs.pegaprox.com/management/multi-cluster/
- https://docs.pegaprox.com/configuration/proxmox-permissions/
- https://github.com/PegaProx/project-pegaprox (Funktionsübersicht)
- `pegaprox/api/realtime.py` und `pegaprox/utils/realtime.py` als Architekturvergleich:
  abonnierte SSE-/WebSocket-Nachrichten, Broadcasts und Aktualisierung nach Aktionen.

Orbit implementiert einen eigenen SSE-Kanal mit zusammengefassten direkten
Proxmox-Node-Statusabfragen und unabhängigem Inventar-/Modulkanal. Ein SSE-Transport
allein garantiert keine frischen CPU-Werte: entscheidend sind Quelle, Abfragezeit,
Pufferung und die Vermeidung späterer Überschreibungen durch alte Antworten.

| Referenzbereich | Orbit 0.13 | Noch zu implementieren / abzunehmen |
| --- | --- | --- |
| Lastanzeige | Direkte Node-/Gastmessungen, Sekundentakt, Alter, SSE, gemeinsame Abfragen | Messung und Lasttest an realen Clustern, Langzeithistorie |
| Multi-Cluster | Inventar, Verbindungen, geprüfte API-Fallbacks | Cross-Cluster-Migration/Transfernetz |
| QEMU | Erstellung, Ressourcen, Optionen, NICs, Disk-Workflows, Snapshots, Konsole | Vollständige EFI/TPM/PCI/USB/Cloud-init-/Device-Editoren |
| Container | Erstellung/Restore in neue CTID, VNC-Terminal, Ressourcen/Swap, NIC/IP/DNS, Mountpoints, Optionen/Features, Snapshot, Backup, Migration | Weitere CT-/Mountpoint-Features, Terminalkomfort und Realabnahme |
| Storage | Konfiguration bearbeiten, ausgewählte Backends einbinden, Inhalte lesen | Credentials, Ceph, Pools/VGs, Upload/Löschen |
| Netzwerk | Linux-Bridge/Bond/VLAN und explizite Aktivierung | OVS/SDN/EVPN, vollständige Topologie |
| Firewall | Datacenter/Host/Gast, Regeln und Optionen | Alias-/IPSet-/Security-Group-Editoren, Logs |
| Sicherung | Einzelgast-Backups/Restore, PBS-Status | Zeitpläne/Retention, automatisierte Restore-Prüfung |
| Replikation | Übersicht | Jobs, Snapshot-Policies, storageübergreifende Replikation |
| Hochverfügbarkeit | Proxmox-HA-Zielzustände | Eigene HA-Orchestrierung, Affinität und DR-Abläufe |
| Wartung | Paket-/Versionsübersicht | Evakuierung, Rolling Updates, Hostdienste und Shell |
| Zugang | Ein Administrator, Sitzungen und CSRF | Benutzer/Rollen/Tenants, LDAP/OIDC, Clientportal |
| Verteilung | Einzelne Migration im Cluster | Automatischer Lastausgleich und Planer |
| Andere Hypervisoren | Keine Verwaltung | XCP-ng, ESXi und plattformübergreifende Migration |
| Berichte | Aktuelle Kapazität | Kosten, Energie, Kapazitätsprognosen, Exporte, Compliance/CVE |
| Benachrichtigungen | Aufgabenprotokoll und Fehleranzeige | Alarmregeln, Mail/Webhooks/SIEM |
| Erweiterungen | Orbit-Registry/Blueprints | Vollständige Plugin-API und weitere Integrationen |

## Grenzen der Prüfung

Automatisierte Tests verwenden lokale Fake-Proxmox-Server, echte HTTP-SSE-
Verbindungen und DOM-Prüfungen. Sie prüfen Parameter, Konflikte, Task-Verfolgung,
fehlende Wiederholungen und UI-Zustand. Sie ersetzen keine Abnahme der Storage-
Backends, Gast-Hotplug-/Pending-Semantik oder Proxy-Konfiguration in einer echten
Installation. Es wurden keine echten Hosts/Gäste gestartet, gestoppt oder
umkonfiguriert.

Nutanix-orientierte Anforderungen und Definition von „abgenommen“: [ACCEPTANCE_MATRIX.md](ACCEPTANCE_MATRIX.md).
