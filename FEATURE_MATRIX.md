# Funktionsumfang und verbleibende Lücken · Go 0.11

Ziel ist eine eigenständige zentrale Proxmox-Oberfläche. Der aktuelle Stand ist
noch keine vollständige Funktionsgleichheit mit Proxmox, PegaProx oder Nutanix.
Die Oberfläche nutzt eine Nutanix-orientierte Struktur mit Produktleiste,
einer Hauptnavigation, Kontextauswahl, Ressourcenübersichten und Aufgaben.

| Bereich | Implementiert | Weiterhin offen |
| --- | --- | --- |
| Live-Anzeige | SSE, laufendes Inventar/Aufgaben/Module, ausgewählte VM-Details, Wiederverbindung, unveränderte Formulare/Konsole | Echte Cluster-Abnahme und Lasttest mit vielen parallelen Clients |
| Host-Netzwerk | Linux-Bridge/Bond/VLAN, IPv4/IPv6, Gateway, MTU, Ports, VLAN-Basis, explizites Anwenden/Verwerfen, Diff | Vollständige OVS-, VXLAN-/EVPN-/SDN-, Controller-/Subnetz- und DHCP-Editoren |
| Firewall | Regeln und Optionen für Datacenter/Host/QEMU/LXC, Erstellen/Bearbeiten/Löschen/Umordnen, Digest-/Revisionsprüfung | Vollständige Alias-/IPSet-/Security-Group-Editoren, alle erweiterten Backend-Optionen und Logs |
| VM | Grundhardware, CPU/RAM, NIC/VLAN, ISO, Snapshot, Backup/Restore, Migration, HA, Erstellung, Löschen, noVNC | Vollständige PCI-/USB-/EFI-/TPM-/Cloud-init-/CPU-Modell- und weitere Proxmox-Detailkonfiguration |
| LXC | Inventar, Grundverwaltung, Snapshots, Backup, gestoppte Migration, HA | Erstellung, Terminal und vollständiger Ressourcen-/Mountpoint-Editor |
| Storage | Konfiguration/Kapazität lesen, Image-Import-Inhaltstyp freigeben | Vollständige Storage-/Ceph-/Volume-Verwaltung |
| Sicherung/HA | VM-Aktionen, HA-Gastzustände, Status/Jobs lesen | Backup-Zeitplan-, Replikations- und vollständige HA-Regel-Editoren, orchestrierte DR-Pläne |
| Hosts/Lifecycle | Versionen/Updateübersicht lesen | DNS/NTP/Repositories/Dienste, geprüfte Updates/Upgrades und Host-Shell |
| Identität | Ein Administrator, Passwort, Sitzungen, CSRF | Mehrere Benutzer, RBAC, SSO, Proxmox-Permissions-/Pool-Editoren |
| Registry | Web-/Cache-Modus, geprüfte Images, VE-9+-Import, Blueprints | Vollständige Offline-Paketquelle und Abnahme an realen Gästen |
| Externe Plattformdienste | Schnittstellenumfang dokumentiert | Files/Objects/Kubernetes/Firmware-/Hersteller-Lifecycle |

## Vorhandenes Coterm

Primärquellen: https://github.com/ConvoyPanel/coterm und dessen `develop`-Quellcode
unter `src-rust/src/routes/websocket.rs`, `src-rust/src/api/novnc.rs` und
`src-rust/src/api/proxmox.rs` (geprüft am 2. November 2026).

Coterm ist ein Konsolenproxy für Convoy. Ein Browser sendet ein vom gemeinsamen
Geheimnis signiertes JWT als `token`-Cookie. Coterm ruft über `CONVOY_URL` den
Callback `/api/coterm/servers/{uuid}/create-console-session` auf und erwartet
unter anderem `pve_auth_cookie`; seine Proxmox-WebSocket-Verbindung verwendet
`PVEAuthCookie`. Orbit speichert API-Token und verwendet ein eigenes, an die
Orbit-Sitzung gebundenes Gateway. Eine Coterm-URL allein ist deshalb kein
funktionierender Austausch für dieses Gateway.

Ein Adapter müsste das Callback-/JWT-Protokoll und die abweichende Proxmox-
Authentifizierung sauber lösen. Die Konfiguration eines bereits mit Convoy
verbundenen Coterm sollte dafür nicht ungeprüft verändert werden. In 0.11
bleibt Orbits Go-Gateway aktiv; Coterm wird weder installiert noch
umkonfiguriert, und es gibt keinen funktionslosen Provider-Schalter.
