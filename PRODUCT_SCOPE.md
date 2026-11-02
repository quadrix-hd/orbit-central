# Orbit Central – Zielbild und nächster Ausbau

Stand: 2. November 2026. Version 0.6.0. Eigenständige Implementierung für Proxmox, mit Prism Central als Bedienreferenz und PegaProx als ergänzender Funktionsreferenz.

## Referenzen

- Prism: https://www.nutanix.com/products/prism
- PegaProx-Funktionsübersicht: https://docs.pegaprox.com/getting-started/overview/
- PegaProx-Projekt: https://github.com/PegaProx/project-pegaprox

Die öffentlich auffindbare PegaProx-Übersicht nennt Multi-Cluster, VM-/Container-Lifecycle, Konsolen, Bulk-Aktionen, PBS, HA/DR, Rolling Updates, Monitoring und Rollen/SSO. Die direkten Dokumentseiten lieferten beim Abruf teilweise HTTP 403; berücksichtigt wurden die verfügbaren offiziellen Suchauszüge. Es wurde kein PegaProx-Code in dieses Projekt übernommen.

## Bereits in 0.5.0

- Kompakte helle Verwaltungsoberfläche, Arial/Systemschriften, direkte VM-Tabelle und Detaildialoge.
- Eine direkte API-Verbindung pro VE-Cluster und separate PBS-Verbindungen.
- Compute-Umgebungsfilter, VM-Inventar, Status und Ressourcenübersicht.
- VM-Erstellung von ISO, Hardware/NICs, Start/Shutdown, Snapshot/Rollback, Backup/Restore, Migration und bestätigtes Löschen.
- noVNC und ein sitzungsgebundenes Gateway implementiert; echte VE-Konsolenanbindung noch nicht abgenommen.
- Sammelaktionen für bis zu 20 ausgewählte VMs.
- Eigener Cloud-init-Marketplace mit gespeicherten Blueprints und vollständigen Klonen.
- Lesende Storage-, Netzwerk-, Firewall/SDN-, HA-/Replikations-, PBS- und Paketübersichten.
- Persistente Aufgaben, Wiederaufnahme bekannter UPIDs und dokumentierte manuelle Prüfung unklarer Übermittlungen.
- Docker-Projekt, lokaler Administratorzugang, verschlüsselte Tokens und Backup-/Update-Skripte.

## Nächste sinnvolle Ausbaustufen

| Priorität | Bereich | Benötigte Umsetzung und Abnahme |
|---|---|---|
| 1 | Reale Testumgebung | VE/PBS-Versionen, ACLs, CA, Storage-Fähigkeiten und Migration auf einem Docker-Testhost abnehmen |
| 2 | Vollständige VM-Arbeitsabläufe | Reale Konsolen-/Recovery-Abnahme, UEFI/Boot/PCI-Geräte, LXC und fehlende Hardwarefälle |
| 3 | Betrieb bei mehreren Benutzern | Rollen, Rechte pro Cluster/VM-Pool, auditierbare Freigaben und SSO; Trennung von Admin, Operator und Self-Service |
| 4 | Sammelaktionen und Monitoring | Ausgewählte VMs als Batch, pro-VM-Vorprüfung, Ergebnisübersicht, Alerts und aktualisierte Metriken |
| 5 | Schutz und Recovery | PBS-Verify-/Retention-/Sync-Workflows, Restore-Proben und orchestrierte Recovery-Pläne mit nachgewiesener Ausführbarkeit |
| 6 | Rolling Updates | Paket-/Versionsprüfung, Quorum-/Kapazitätsprüfung, Evakuierung, Wartungsfenster, Host-Upgrade und klar definierter Rückweg |
| 7 | Zusatzdienste | Separate Provider für Files/NFS/SMB, S3/Objects, Block-Volumes, Kubernetes-Lifecycle und Hardware-/Firmware-Management |

Diese Tabelle beschreibt ausstehende Implementierung. Sie ist keine Anzeige fertiger Funktionen und keine Terminzusage.

## Produktregeln

1. Ein Status muss aus einer echten Schnittstelle stammen oder deutlich als Demo gekennzeichnet sein.
2. Fehlende Rechte und nicht erreichbare Provider dürfen nicht als leerer, gesunder Zustand erscheinen.
3. Ein sichtbarer Aktionsknopf braucht einen vollständigen, protokollierten Backend-Ablauf.
4. Ein unklarer Übermittlungszustand darf keine automatische Wiederholung einer möglichen Verwaltungsaktion auslösen.
5. Native Proxmox-Funktionen und Zusatzprodukte bleiben technisch sauber getrennt.
6. Ein Menüeintrag ist kein Ersatz für einen implementierten Provider und eine reale Abnahme.


## Ergänzungen 0.6.0

- Direkte VE-Verbindung mit mehreren konfigurierten und geprüften Cluster-Endpunkten; kein PDM benötigt. Lese-Failover, sichere Nichtwiederholung bereits versendeter Schreibaktionen.
- LXC-Grundverwaltung: Details, Start/Shutdown/Reboot, CPU/RAM, Snapshots, Backup und Offline-Migration. Erstellung, Konsole, Netzwerk, Mountpoints und Restore bleiben offen.
- VM-/CT-HA-Ressource anlegen oder Zielzustand ändern. Gruppen, Regeln, Wartungsmodus und automatisierte HA-Eignungsprüfung bleiben offen.
- Orbit selbst: dokumentierter Betrieb als HA-VM mit Wiederanlaufzeit. Keine aktive/aktive Replikation, kein unterbrechungsfreies Dashboard-Failover.
