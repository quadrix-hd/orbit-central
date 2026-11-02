# Zentraler Marketplace: lokaler Spiegel

Stand 0.7.0: Der lokale Download- und Aktualisierungsweg ist implementiert. Die Registry-Quelle ist https://quadrix-hd.github.io/orbit-central/catalog.json und enthält zunächst Cloud-init-Rezepte. Die automatische Proxmox-Bereitstellung zentraler Pakete ist ausdrücklich noch nicht implementiert. Die bisherige lokale Cloud-init-Blueprint-Bereitstellung bleibt als erweiterte Funktion bestehen.

## Herausgeber statt Endnutzer

Der Herausgeber hostet eine HTTPS-JSON-Datei und die Paketdateien auf derselben Origin (Hostname und Port). Die Katalogadresse wird bei der Installation über `ORBIT_CATALOG_URL` vorgegeben. Endnutzer erstellen die Einträge nicht selbst. Die Docker-Konfiguration verwendet diese Registry als Standard. Über ORBIT_CATALOG_URL kann der Herausgeber eine andere HTTPS-Quelle vorgeben.

In `.env`:

```
ORBIT_CATALOG_URL=https://quadrix-hd.github.io/orbit-central/catalog.json
```

Orbit synchronisiert beim Programmstart, also auch nach einem App-Update. Ein bestehender lokaler Katalog bleibt während des Downloads verfügbar. Der erste Download läuft im Hintergrund; der Marketplace zeigt den Zustand. Eine defekte oder nicht erreichbare Quelle blockiert den übrigen Dienst nicht. Der Operator kann eine Synchronisierung im Marketplace manuell anstoßen. Es gibt keinen täglichen Timer.

## Transportformat v1

Dieses Format beschreibt den Cache, noch keine Bereitstellungsrezepte. Beispiel für einen kleinen, prüfbaren Transporttest: Die Datei `assets/hello.txt` enthält exakt die fünf Bytes `hello`, ohne Zeilenumbruch.

```json
{
  "schema": 1,
  "revision": "2026-11-02.1",
  "packages": [
    {
      "id": "transport-test",
      "name": "Transporttest",
      "version": "1.0",
      "description": "Prüft den lokalen Paketdownload, erstellt keine VM.",
      "category": "Test",
      "assets": [
        {
          "name": "hello.txt",
          "url": "assets/hello.txt",
          "size": 5,
          "sha256": "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
        }
      ]
    }
  ]
}
```

Dateigrößen müssen exakt stimmen. SHA-256 kann auf dem Herausgeber-Server mit `sha256sum DATEI` berechnet werden. Relative URLs beziehen sich auf die Manifest-Adresse. Alle Dateien müssen von derselben HTTPS-Origin stammen; Redirects, HTTP, URLs mit Zugangsdaten und Query-Parametern werden nicht akzeptiert. Öffentlich vertrauenswürdiges TLS verwenden. Das Katalogsystem schickt keine Proxmox-Zugangsdaten an die Quelle.

## Lokaler Betrieb und Fehlerverhalten

- Manifest maximal 2 MiB, maximal 500 Pakete und 20 Dateien pro Paket.
- Maximal 20 GiB an eindeutigen Dateien pro Katalogrevision; außerdem wird freier lokaler Speicher geprüft. Größere VM-Sammlungen benötigen eine abgestimmte Kapazitätskonfiguration.
- Streaming-Download statt Laden kompletter VM-Images in den RAM.
- Dateien werden nach Größe und SHA-256 geprüft. Bereits gültige Dateien werden wiederverwendet; beschädigte Dateien werden erneut geladen.
- Erst wenn alle Dateien gültig vorliegen, wird die aktive Katalogrevision atomar ersetzt. Bei Abbruch bleibt die letzte gültige Revision aktiv.
- Katalog und Dateien liegen im persistenten Orbit-Datenvolume unter `catalog/`. Nach erfolgreichem Download sind Anzeige und Dateidownload ohne Verbindung zur Herausgeberseite möglich. Der lokale Orbit-Dienst muss weiter laufen.
- Pakete werden nicht automatisch entpackt oder ausgeführt. Es werden keine VMs geändert, gelöscht oder aktualisiert. Eine neue Paketversion verändert keine vorhandene Gastinstallation.
- Alte Inhaltsdateien werden noch nicht automatisch entfernt. Zusätzlichen Platz für aufeinanderfolgende Revisionen einplanen.
- Die bisherigen Orbit-Datenbank-Backups enthalten den Cache nicht. Für vollständigen Offline-Restore zusätzlich das gestoppte Datenvolume sichern oder die Pakete nach Wiederherstellung erneut synchronisieren.

## Noch benötigt

Die tatsächliche URL und ein Beispiel des vorgesehenen Pakets: Proxmox-QEMU-Backup (`.vma.zst`), QCOW2-Cloud-Image, ISO, LXC-Vorlage oder ein anderes Format. Daraus folgen Import, Storage-Upload, VM-Konfiguration und Cloud-init-Abläufe. Ohne diese Information wird kein nur scheinbar funktionierender „Installieren“-Knopf angeboten. Bestehende lokale Blueprints bleiben im Reiter „Eigene Vorlagen · erweitert“ verfügbar.
