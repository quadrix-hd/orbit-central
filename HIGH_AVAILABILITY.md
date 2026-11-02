# Orbit und Node-Ausfälle

Orbit 0.6.0 unterscheidet API-Erreichbarkeit, Gast-HA und die Verfügbarkeit des Orbit-Dienstes selbst.

## 1. Ausfall des angesprochenen Proxmox-API-Hosts

Unter Einstellungen die VE-Verbindung bearbeiten. Die primäre HTTPS-Adresse bleibt bestehen. Unter „Weitere Node-Adressen“ die Adressen anderer Nodes desselben Clusters eintragen, jeweils eine pro Zeile. Beispiel:

```
https://pve-02.example.lan:8006
https://pve-03.example.lan:8006
```

Alle Adressen müssen bei der Einrichtung erreichbar sein, gültige Zertifikate haben und zum gleichen Cluster gehören. Orbit vergleicht Clustername und Node-Mitglieder. Der Token muss auf allen Adressen gültig sein; ein Cluster-Token genügt. Kein Proxmox Datacenter Manager erforderlich. Nicht automatisch aus fremden Netzwerkdaten abgeleitete Adressen verwenden.

Lesende Anfragen wechseln nach Transportfehlern oder unklaren Serverfehlern zum nächsten konfigurierten Endpunkt. Der letzte erfolgreiche Endpunkt wird im laufenden Prozess bevorzugt. Nach Neustart wird wieder mit der primären Adresse begonnen. Maximal acht Endpunkte. Pro Leseversuch gilt ein Timeout von acht Sekunden; der Wechsel erfolgt deshalb nicht zwingend sofort. Berechtigungsfehler werden nicht durch Wechsel umgangen.

Vor schreibenden Anfragen wird ein erreichbarer Endpunkt ausgewählt. Nach dem Versand wird eine schreibende Anfrage niemals automatisch an einen zweiten Host geschickt. Ein verlorenes Ergebnis bleibt prüfbedürftig. Ein VNC-Ticket bleibt an den tatsächlich verwendeten API-Endpunkt gebunden; bestehende Konsolen werden bei Hostausfall nicht transparent übertragen.

## 2. Gast-Verfügbarkeit mit Proxmox HA

Im VM-/LXC-Arbeitsbereich „HA verwalten“ öffnen, gewünschten Zustand und Gastnamen bestätigen. Orbit kann eine HA-Ressource anlegen oder deren Zielzustand ändern. Beim Einplanen und direkt vor Ausführung wird Cluster-Quorum verlangt. „Gestartet“ kann einen Gast starten, „Gestoppt“ oder „Deaktiviert“ können ihn stoppen. „Ignoriert“ beendet die aktive HA-Steuerung für diese Ressource. Das ist keine vollständige HA-Eignungsprüfung.

Voraussetzungen müssen im Cluster bereits eingerichtet sein: Quorum, Watchdog/Fencing, geeignete Host-Ressourcen und ein auf dem Ziel verfügbarer Storage. Gruppen, Regeln und Wartungsabläufe verwaltet diese Version noch nicht. Ein erfolgreich gespeicherter Zielzustand bedeutet nicht, dass ein Failover bereits getestet wurde.

## 3. Orbit selbst erreichbar halten

Empfohlener einfacher Betrieb:

1. Eine eigene Linux-VM für Docker/Orbit verwenden. Docker mit dem Betriebssystem starten lassen.
2. Sämtliche VM-Datenträger, einschließlich des Datenträgers mit dem Orbit-Datenvolume, auf einem für die HA-Zielnodes verfügbaren Storage bereitstellen. Das allein garantiert noch keine Ausfallsicherheit des Storage.
3. Die Orbit-VM als Proxmox-HA-Ressource konfigurieren. Sie kann nach der ersten Anbindung auch über Orbits „HA verwalten“-Aktion registriert werden.
4. Einen festen DNS-Namen und eine feste VM-Adresse verwenden. Ein Reverse-Proxy auf derselben VM startet mit ihr neu; ein externer Proxy und DNS müssen selbst verfügbar sein.
5. An einer Testumgebung den kontrollierten Ausfall einer Node durchspielen: Wiederanlauf der Orbit-VM, Login, Wechsel des API-Endpunkts und Wiederaufnahme vorhandener Aufgaben prüfen. Auch den Ausfall des zuerst konfigurierten API-Hosts gesondert testen.

**Grenze:** Eine HA-VM startet nach Fehlererkennung auf einer anderen Node neu. Währenddessen ist Orbit nicht erreichbar. Das entspricht keinem gleichzeitig laufenden Dashboard auf jeder Cluster-Node. Aktive/aktive Orbit-Instanzen, Datenbankreplikation und ein durchgängiger Load-Balancer-Failover sind nicht implementiert. SQLite und das Datenvolume dürfen nicht unkoordiniert von mehreren Orbit-Prozessen gleichzeitig verwendet werden. `restart: unless-stopped` schützt nur vor einem Containerabbruch auf einem noch laufenden Host.

## Noch erforderliche Abnahme

API-Failover, schreibende Aktionen nach Umschaltung, Orbit-VM-Neustart nach Hostverlust, Quorum-Verlust, Storage-Ausfall und Wiederaufnahme gespeicherter Aufgaben müssen mit dem Zielcluster getestet werden. Die automatisierten Tests verwenden simulierte API-Antworten; diese Datei ist kein Nachweis einer durchgeführten HA-Abnahme.

Referenz: Proxmox VE HA Manager, offizieller Quelltext der Dokumentation: https://github.com/proxmox/pve-docs/blob/master/ha-manager.adoc
