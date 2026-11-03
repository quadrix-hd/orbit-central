# Produktumfang

Orbit Go 0.9 ist eine Verwaltungsoberfläche für einen Administrator. Der
vollständige zuvor implementierte Backend-Umfang wurde nach Go portiert;
der Standardcontainer benötigt kein Python. Das ist keine vollständige
Neuimplementierung sämtlicher Proxmox- oder Nutanix-Produkte.

Aktuelle Funktionen, Installation und Einschränkungen: README.md.
Prüfnachweise und offene reale Integrationstests: QA_REPORT.md.
Marketplace-Protokoll und Betriebsarten: MARKETPLACE_SOURCE.md.
Cluster-/Host-Verfügbarkeit: HIGH_AVAILABILITY.md.

Noch nicht implementiert: LXC-Erstellung und -Terminal, komplette Host-/Netzwerk-/
Firewall-/Storage-Editoren, Host-Upgrades, mehrere Orbit-Benutzer mit Rollen,
SSO und eine vollständig automatische Hochverfügbarkeit des Orbit-Dockerhosts.

Ab 0.11: Live-Streams und direkte Linux-Netzwerk-/Firewall-Regelbearbeitung
sind implementiert. Die weiterhin offenen Teilbereiche sind präzisiert in
FEATURE_MATRIX.md; die ältere allgemeine Einstufung als rein lesende Module
gilt für diese implementierten Teilbereiche nicht mehr.

Go 0.12 erweitert direkte Lastmessung sowie Storage-/QEMU-/LXC-Konfiguration. Vollständiger PegaProx-Ersatz ist das Ziel, nicht der aktuelle Fertigstellungsstand. Die offenen Bereiche stehen in PEGAPROX_PARITY.md.

Go 0.13 ergänzt LXC-Erstellung, Restore, VNC-Terminal und Feature-Flags. ACCEPTANCE_MATRIX.md unterscheidet Implementierung, lokale Tests und reale Abnahme.
