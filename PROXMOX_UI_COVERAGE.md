# Ersetzt Orbit die Proxmox-Weboberfläche? · Stand 3. November 2026

Kurzantwort: **Für den täglichen Betrieb von VMs und Containern weitgehend ja,
für Einrichtung und Pflege der Hosts und des Clusters noch nicht.** Wer heute
umsteigt, braucht die Proxmox-Oberfläche weiterhin für die unten mit ✗
markierten Aufgaben, vor allem Benutzer/Rechte, Datenträger/ZFS/Ceph,
Backup-Zeitpläne, ISO-Upload, Host-Shell und das Einspielen von Updates.

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
| Backup-Jobs (Zeitpläne) | ◐ | Nur Anzeige; Anlegen/Bearbeiten fehlt |
| Replikation | ◐ | Nur Anzeige |
| Hochverfügbarkeit | ◐ | HA-Zustand pro Gast; HA-Gruppen/-Regeln fehlen |
| Firewall | ◐ | Regeln und Optionen; Aliase, IPSets, Security Groups fehlen |
| SDN (Zonen, VNets, Subnetze) | ✗ | Nur Lesen der VNets |
| Benutzer, Gruppen, Rollen, Pools, Realms, 2FA | ✗ | Orbit selbst hat nur einen Administrator |
| Cluster erstellen/beitreten, Optionen, ACME, Metrik-Server, Resource Mappings | ✗ | |

## Host (Node)

| Bereich | Stand | Bemerkung |
| --- | --- | --- |
| Status, CPU/RAM live | ✅ | |
| Verlaufsdiagramme (RRD) | ✗ | Nur aktuelle Werte |
| Netzwerk (Linux-Bridge/Bond/VLAN) | ✅ | Mit Diff und explizitem Anwenden |
| Host-Firewall | ✅ | |
| Updates | ◐ | Anzeige; Aktualisieren/Upgrade und Repositories fehlen |
| Shell | ✗ | Konsolen-Gateway existiert, Host-Shell nicht angebunden |
| Datenträger, LVM, ZFS, SMART | ✗ | |
| Ceph | ✗ | |
| DNS, Hosts, Zeit, Syslog, Zertifikate | ✗ | |
| Host neu starten/herunterfahren, Wartungsmodus | ✗ | Steht in der Roadmap (Rolling Updates) |
| Sammelaktionen (Start/Stopp/Migration) | ◐ | Über die VM-Liste, nicht pro Host |

## Virtuelle Maschine (QEMU)

| Bereich | Stand | Bemerkung |
| --- | --- | --- |
| Erstellen | ◐ | Grundkonfiguration; UEFI/TPM/Cloud-init im Assistenten fehlen |
| Start, Herunterfahren, Neustart | ✅ | |
| Hart ausschalten, Reset, Pausieren, Fortsetzen | ✅ | Neu |
| Klonen (voll/verknüpft), in Vorlage umwandeln | ✅ | Neu |
| Migrieren, Löschen | ✅ | |
| Konsole (noVNC) | ✅ | SPICE und serielle xterm.js-Konsole fehlen |
| CPU, RAM, Ballooning, CPU-Typ, Sockets | ✅ | |
| Disks hinzufügen/vergrößern/verschieben | ✅ | |
| Netzwerkkarten, ISO/CD | ✅ | |
| EFI, TPM, PCI/USB-Passthrough, Serial, Audio | ✗ | |
| Cloud-init bearbeiten | ✗ | Nur über Marketplace-Bereitstellung |
| Optionen (Boot, Autostart, Agent, Machine) | ✅ | |
| Snapshots | ✅ | |
| Backup, Restore | ✅ | Manuell; Zeitpläne siehe oben |
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
| Migration | ◐ | Nur gestoppt; Neustart-Migration fehlt |

## Storage-Inhalte und Backup Server

| Bereich | Stand | Bemerkung |
| --- | --- | --- |
| Inhalte ansehen | ✅ | |
| ISO/Vorlagen hochladen, Download von URL | ✗ | Intern für den Marketplace vorhanden, nicht als Funktion |
| Volumes löschen | ✗ | |
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
| Events und Audit-Log | ◐ Audit vorhanden, Export fehlt |
| Alarme mit Regeln und Benachrichtigungen | ✅ Neu |
| DRS mit Affinitätsregeln | ◐ Neu: Empfehlungen + Anti-Affinität; automatischer Modus fehlt |
| Rolling Updates | ✗ |
| Cross-Cluster-Migration | ✗ |
| Migration von ESXi/vCenter/Hyper-V/Nutanix/XCP-ng | ✗ |
| RBAC, LDAP, OIDC | ✗ |
| Compliance- und CVE-Prüfung | ✗ |
| Berichte, Änderungsverfolgung | ✗ |
| Site Recovery | ✗ |
| KI-Assistent | ✗ |

## Reihenfolge der nächsten Schritte

Ausgerichtet auf „Proxmox-Oberfläche nicht mehr nötig“ und Firmeneinsatz:

1. Benutzer, Rollen, OIDC/LDAP in Orbit (Voraussetzung für Teams).
2. Backup-Zeitpläne anlegen/bearbeiten, PBS-Prune/Verify.
3. ISO/Vorlagen hochladen und per URL laden, Volumes löschen.
4. Host-Shell, Host-Neustart, Wartungsmodus mit Evakuierung, Rolling Updates.
5. Datenträger/ZFS/LVM, danach Ceph-Übersicht.
6. RRD-Verlaufsdiagramme für Host und Gast.
7. Cloud-init-Editor, EFI/TPM/PCI/USB im VM-Editor.
8. SDN, Firewall-Aliase/IPSets/Security Groups, Topologie-Karte.
9. Cross-Cluster-Migration, danach Import aus ESXi/Hyper-V.
