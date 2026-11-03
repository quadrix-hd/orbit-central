# Ersetzt Orbit die Proxmox-Weboberfläche? · Stand 3. November 2026

Kurzantwort: **Für den täglichen Betrieb von VMs, Containern, Backups und
Host-Wartung ja; für die Ersteinrichtung von Hosts und Cluster noch nicht.**
Seit diesem Stand deckt Orbit Benutzer/Rollen/2FA/SSO, Backup-Zeitpläne,
Restore-Tests, ISO-Upload, Host-Shell, Updates, Wartungsmodus und
Rolling-Neustarts ab. Die Proxmox-Oberfläche bleibt nötig für die unten mit ✗
markierten Aufgaben, vor allem Datenträger/ZFS/Ceph, Cluster-Einrichtung,
SDN und Proxmox-eigene Rechte.

Grundlage ist ein Abgleich der Bereiche der Proxmox-VE-Oberfläche mit dem
Orbit-Code (Backend-Routen und -Aktionen, Oberfläche) dieses Branches.
✅ vorhanden · ◐ teilweise · ✗ fehlt. „Vorhanden“ heißt implementiert und
gegen simulierte Proxmox-Antworten getestet; die reale Abnahme an einem
Cluster steht laut [ACCEPTANCE_MATRIX.md](ACCEPTANCE_MATRIX.md) noch aus.

## Rechenzentrum / Cluster

| Bereich | Stand | Bemerkung |
| --- | --- | --- |
| Übersicht, Kapazität, Zustand | ✅ | Dashboard, Live-Messwerte im Sekundentakt |
| Mehrere Cluster in einer Oberfläche | ✅ | Mehr als die Proxmox-GUI |
| Aufgaben (Task-Liste, Logs) | ✅ | Neu: Proxmox-Tasks aller Cluster mit Filter und Live-Log |
| Alarme und Benachrichtigungen | ✅ | Neu: Regeln, Teams/Slack/Discord/ntfy/Webhook/E-Mail |
| Lastverteilung (DRS) | ✅ | Neu: Empfehlungen, Anti-Affinität per Tag, Ausführung per Klick |
| Storage-Konfiguration | ◐ | Directory, NFS, LVM-thin, ZFS anlegen/bearbeiten; CIFS, iSCSI, Ceph/RBD, PBS fehlen |
| Backup-Jobs (Zeitpläne) | ✅ | Anlegen, Bearbeiten, Löschen mit Aufbewahrung; zusätzlich Restore-Tests |
| Replikation | ◐ | Nur Anzeige |
| Hochverfügbarkeit | ◐ | HA-Zustand pro Gast; HA-Gruppen/-Regeln fehlen |
| Firewall | ◐ | Regeln und Optionen; Aliase, IPSets, Security Groups fehlen |
| SDN (Zonen, VNets, Subnetze) | ✗ | Nur Lesen der VNets |
| Benutzer, Rollen, 2FA, SSO für Orbit | ✅ | Admin/Operator/Leser, TOTP, OIDC, API-Tokens |
| Proxmox-Benutzer, Gruppen, Pools, Realms | ✗ | Rechte des Orbit-API-Tokens gelten |
| Geplante Aktionen | ✅ | Neu: Power, Snapshot mit Aufbewahrung, Backup, Restore-Test nach Zeitplan, per VMID oder Tag |
| Tags | ✅ | Neu: anzeigen, filtern, bearbeiten (Proxmox-Tags) |
| Cluster erstellen/beitreten, Optionen, ACME, Metrik-Server, Resource Mappings | ✗ | |

## Host (Node)

| Bereich | Stand | Bemerkung |
| --- | --- | --- |
| Status, CPU/RAM live | ✅ | |
| Verlaufsdiagramme (RRD) | ✅ | Neu: Stunde bis Jahr für Host, VM und Container |
| Netzwerk (Linux-Bridge/Bond/VLAN) | ✅ | Mit Diff und explizitem Anwenden |
| Host-Firewall | ✅ | |
| Updates | ◐ | Anzeige, Paketliste aktualisieren, Installation per Host-Shell; Repositories fehlen |
| Shell | ✅ | Neu: Host-Shell (nur Administratoren, protokolliert) |
| Datenträger, LVM, ZFS, SMART | ✗ | |
| Ceph | ✗ | |
| DNS, Hosts, Zeit, Syslog, Zertifikate | ✗ | |
| Wartungsmodus mit Evakuierung, Host-Neustart | ✅ | Neu: Live-Migration aller Gäste, Rückführung, Rolling-Neustart; Herunterfahren fehlt |
| Sammelaktionen (Start/Stopp/Migration) | ◐ | Über die VM-Liste, nicht pro Host |

## Virtuelle Maschine (QEMU)

| Bereich | Stand | Bemerkung |
| --- | --- | --- |
| Erstellen | ◐ | Grundkonfiguration; UEFI/TPM/Cloud-init im Assistenten fehlen |
| Start, Herunterfahren, Neustart | ✅ | |
| Hart ausschalten, Reset, Pausieren, Fortsetzen | ✅ | Neu |
| Klonen (voll/verknüpft), in Vorlage umwandeln | ✅ | Neu |
| Migrieren, Löschen | ✅ | |
| Konsole (noVNC) | ✅ | Text einfügen, Strg+Alt+Entf, Vollbild; SPICE und xterm.js fehlen |
| CPU, RAM, Ballooning, CPU-Typ, Sockets | ✅ | |
| Disks hinzufügen/vergrößern/verschieben | ✅ | |
| Netzwerkkarten, ISO/CD | ✅ | |
| EFI, TPM, PCI/USB-Passthrough, Serial, Audio | ✗ | |
| Cloud-init bearbeiten | ✗ | Nur über Marketplace-Bereitstellung |
| Optionen (Boot, Autostart, Agent, Machine) | ✅ | |
| Snapshots | ✅ | |
| Backup, Restore | ✅ | Manuell, nach Zeitplan, automatischer Restore-Test |
| Änderungsprotokoll | ✅ | Neu: Orbit-Aktionen mit Benutzer und Proxmox-Tasks aller Hosts |
| Gast-Firewall | ✅ | |
| Replikation, Rechte pro Gast | ✗ | |

## Container (LXC)

| Bereich | Stand | Bemerkung |
| --- | --- | --- |
| Erstellen, Restore | ✅ | |
| Start, Herunterfahren, Neustart, Hart ausschalten | ✅ | |
| Klonen, in Vorlage umwandeln | ✅ | Neu |
| Konsole | ✅ | |
| Ressourcen, Netzwerk, Mountpoints, Optionen/Features | ✅ | |
| Snapshots, Backup | ✅ | |
| Migration | ✅ | Gestoppt oder als Neustart-Migration |

## Storage-Inhalte und Backup Server

| Bereich | Stand | Bemerkung |
| --- | --- | --- |
| Inhalte ansehen | ✅ | |
| ISO/Vorlagen hochladen, Download von URL | ✅ | Mit Fortschritt, Prüfsumme optional |
| Volumes löschen | ✅ | ISO, Vorlagen, Import-Images, Backups; Gast-Disks bewusst nicht |
| PBS: Datastore-Belegung, Tasks | ✅ | |
| PBS: GC/Verify/Prune-Jobs, Backups durchsuchen, Datei-Restore | ✗ | |

## Abgleich mit ProxCenter

Quelle: öffentliche Beschreibungen von ProxCenter (docs.proxcenter.io,
proxcenter.io/editions, GitHub-README), Stand November 2026. Es wurde kein
ProxCenter-Code übernommen (AGPL-3.0); Orbit implementiert eigenständig.

| ProxCenter-Funktion | Orbit |
| --- | --- |
| Multi-Cluster-Dashboard, Inventar, VM-Aktionen, Konsole, Snapshots | ✅ |
| PBS-Verwaltung | ◐ Status und Tasks |
| Ceph-Übersicht | ✗ |
| Netzwerk-Topologie-Karte | ✗ |
| Firewall-Regeln | ✅ |
| IPAM | ✗ |
| Events und Audit-Log | ✅ Audit mit Benutzer, CSV-Export, Änderungsprotokoll pro Gast |
| Alarme mit Regeln und Benachrichtigungen | ✅ Neu |
| DRS mit Affinitätsregeln | ◐ Neu: Empfehlungen + Anti-Affinität; automatischer Modus fehlt |
| Rolling Updates | ✅ Neu: Wartungsmodus, Evakuierung, Rolling-Neustart |
| Cross-Cluster-Migration | ✗ |
| Migration von ESXi/vCenter/Hyper-V/Nutanix/XCP-ng | ✗ |
| RBAC, LDAP, OIDC | ◐ Rollen und OIDC; LDAP fehlt |
| Compliance- und CVE-Prüfung | ✗ |
| Berichte, Änderungsverfolgung | ✅ Neu: CSV-Berichte, Kapazitätsbericht, Änderungsprotokoll |
| Site Recovery | ✗ |
| KI-Assistent | ✗ |

## Reihenfolge der nächsten Schritte

Ausgerichtet auf „Proxmox-Oberfläche nicht mehr nötig“ und Firmeneinsatz:

Erledigt in diesem Stand: Benutzer/Rollen/2FA/OIDC, Backup-Zeitpläne,
Restore-Tests, ISO-Upload und Volumes, Host-Shell, Wartungsmodus und
Rolling-Neustart, RRD-Diagramme, geplante Aktionen, Tags, Berichte,
Änderungsprotokoll, 25 Marketplace-Pakete.

1. Datenträger/ZFS/LVM/SMART, danach Ceph-Übersicht.
2. Cloud-init-Editor, EFI/TPM/PCI/USB im VM-Editor.
3. PBS-Prune/Verify/GC-Jobs, Datei-Restore.
4. LDAP, Proxmox-Rechte/Pools.
5. SDN, Firewall-Aliase/IPSets/Security Groups, Topologie-Karte.
6. Cross-Cluster-Migration, danach Import aus ESXi/Hyper-V.
