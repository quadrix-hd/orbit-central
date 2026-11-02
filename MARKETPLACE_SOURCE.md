# Orbit Registry

Quelle: https://quadrix-hd.github.io/orbit-central/catalog.json
Website: https://quadrix-hd.github.io/orbit-central/

Schema 1 beschreibt versionierte Pakete mit Name, Kategorie und geprüften
Dateien (`name`, `url`, `size`, `sha256`). Ein `deployment` mit Typ `cloud-image`
verweist per `image_sha256` auf eine enthaltene Image-Datei und enthält eine
begrenzte Liste von Distributionspaketen. Beliebige Shellskripte werden nicht
vom Herausgeber ausgeführt.

Go synchronisiert den Katalog beim Start und alle sechs Stunden. `web` lädt
Metadaten und Dateien bei Bedarf; `cache` lädt alle enthaltenen Dateien und
Basisimages lokal. Status, Synchronisierung und Moduswechsel sind im
Marketplace verfügbar. Updates ändern keine bestehenden VMs. Fehlgeschlagene
Prüfsummen- oder Downloadprüfungen erhalten den bisherigen Katalogstand.

Neue Quelle in `.env`: `ORBIT_CATALOG_URL=https://registry.example/catalog.json`.
Der Ursprung der Quelle sowie die ausdrücklich freigegebenen offiziellen
Distributionsserver sind zulässig. Redirects werden nicht verfolgt; bei einer
Umstellung auf einen Mirror muss dessen freigegebene Adresse gepflegt werden.

Die Beispielregistry veröffentlicht Rezeptdateien über Pages; große Images
liegen bei Ubuntu und Debian. Für eigene Imagehaltung die Dateien auf dem
Ursprung der eigenen Registry veröffentlichen und Größe/Prüfsumme pflegen.
GitHub Pages ist kein Container-Registry- oder Image-Upload-Dienst.

Direkte Bereitstellung benötigt Proxmox VE 9+, Datei-Storage mit `import` und
`iso` sowie ein Image-Storage. Orbit erzeugt eine NoCloud-ISO mit eigenem
Benutzer und öffentlichem SSH-Schlüssel. Der Gast bleibt ohne explizite
Startauswahl ausgeschaltet. Anwendungsinstallation benötigt beim ersten Start
Paketquellen im Gast; der lokale Image-Cache ist keine vollständige Offline-
Paketquelle. Ein abgeschlossener VM-Auftrag bestätigt noch keine betriebsbereite
Anwendung. Reale Gäste sind bislang nicht zur Abnahme verwendet worden.
