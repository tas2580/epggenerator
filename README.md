# EPG Generator

Lokaler Webdienst zum Zuordnen von M3U-/M3U8-Sendern zu XMLTV-Programmdaten.
**Alle Playlist-Sender bleiben im erzeugten EPG enthalten.** Nicht zugeordnete Sender haben einen `<channel>`-Eintrag, aber keine `<programme>`-Einträge. Sender, die nur in der EPG-Quelle vorkommen, werden ausgeschlossen.

## Starten

Zum Bauen wird Go 1.19 oder neuer benötigt. Es werden keine externen Go-Pakete oder JavaScript-Abhängigkeiten verwendet.

```bash
go build -buildvcs=false -o epggenerator .
./epggenerator -data-dir ./data
```

Webinterface: **http://localhost:8080**, im Heimnetz `http://SERVER-IP:8080`.
Das gebaute Programm enthält die gesamte Weboberfläche und benötigt keine Go-Installation auf dem Zielrechner. Es muss für dessen Betriebssystem und Architektur gebaut sein.

Der Dienst lauscht standardmäßig auf allen Schnittstellen und ist für ein vertrauenswürdiges Heimnetz ohne Anmeldung gedacht. Für ausschließlich lokalen Zugriff `-listen 127.0.0.1:8080` verwenden.

## Darstellung

Das Webinterface übernimmt standardmäßig den Hell-/Dunkelmodus des Betriebssystems und reagiert auch auf Änderungen während der Nutzung. Über den Design-Button oben rechts lässt sich zwischen **System**, **Hell** und **Dunkel** wechseln. Aus dem Systemmodus schaltet der erste Klick auf die jeweils andere Darstellung. Die Auswahl wird pro Browser gespeichert; **System** aktiviert wieder die automatische Anpassung.

## Listen-Port konfigurieren

In `config.json` den gewünschten Port eintragen, zum Beispiel:

```json
{
  "listenPort": 9090
}
```

Danach den Dienst neu starten und **http://localhost:9090** öffnen. Gültig sind Ports von 1 bis 65535. Die mitgelieferte Datei verwendet Port 8080.

Ohne weitere Optionen liest das Programm `config.json` aus dem aktuellen Arbeitsverzeichnis. Fehlt diese Datei, gilt weiterhin Port 8080. Eine andere Datei kann ausdrücklich angegeben werden:

```bash
./epggenerator -config /pfad/zur/config.json
```

Eine ausdrücklich angegebene Datei muss existieren. Fehlerhafte Konfigurationen verhindern den Start mit einer Fehlermeldung. `-listen`, beispielsweise `-listen 127.0.0.1:9090`, überschreibt den konfigurierten Port und kann zusätzlich die Bind-Adresse einschränken. Die Konfiguration wird beim Start gelesen; Änderungen benötigen einen Neustart.

## Verwendung

1. Unter **Quellen einstellen** die EPG-URL eintragen und als **Playlist-Quelle** entweder **URL** oder **Lokale Datei auf dem Server** wählen. Vorbelegt sind `https://iptv-epg.org/files/epg-de.xml` und `http://tv.lan/tv.m3u8`. Für eine lokale Playlist beispielsweise `/home/user/code/epggenerator/tv.m3u8` als Dateipfad eintragen.
2. **Speichern und aktualisieren** lädt beide Dateien. Einstellungen werden dauerhaft gespeichert. Der Rechner, auf dem der Dienst läuft, muss die URLs erreichen können.
3. **ID-Treffer übernehmen** bestätigt alle noch offenen exakten ID-Treffer. Alternativ einen Vorschlag pro Sender übernehmen oder über **Zuordnen** in allen EPG-Sendern suchen. Vorschläge werden niemals automatisch bestätigt.
4. Im TV-Player die erzeugte EPG-URL und vorzugsweise die passende Zusatz-Playlist eintragen:

| Datei | URL |
| --- | --- |
| EPG (XMLTV) | `http://SERVER-IP:8080/epg.xml` |
| Playlist mit passenden IDs | `http://SERVER-IP:8080/playlist.m3u8` |

Die Zusatz-Playlist enthält alle ursprünglichen Sender, Stream-Adressen und sonstigen Metadaten. Eindeutige `tvg-id` bleiben erhalten; fehlende oder doppelte IDs werden stabil ersetzt. Im Playlist-Kopf wird die EPG-URL auf diesen Dienst gesetzt. Die Originaldateien werden nicht verändert. Die Stream-Adressen werden durchgereicht, der Dienst ist kein Streaming-Proxy.

Eine lokale Playlist liegt auf dem Rechner des Dienstes, nicht auf einem eventuell anderen Rechner mit dem Browser. Sie wird bei jeder manuellen und automatischen Aktualisierung erneut gelesen. Relative Pfade wie `./tv.m3u8` werden beim Speichern relativ zum Arbeitsverzeichnis des Dienstes in absolute Pfade umgewandelt. Ein Datei-Upload ist dafür nicht erforderlich. Fehlt die Datei, ist sie nicht lesbar oder ungültig, bleibt die letzte gültige Ausgabe erhalten.

**Keine Zuordnung** entfernt nur die Programmdaten. Fehlt ein zuvor zugeordneter EPG-Sender nach einer Aktualisierung, wird die Zuordnung als **Zu prüfen** markiert; der Playlist-Sender bleibt ohne Sendungen erhalten. Verschwindet ein Sender aus der Playlist, verschwindet er auch aus dem nächsten erfolgreichen Export. Seine gespeicherte Zuordnung bleibt für eine spätere Rückkehr erhalten.

Sender werden über ihre eindeutige `tvg-id` wiedererkannt, sonst über die genaue Stream-Adresse. Änderungen an dieser Identität erfordern eine neue Zuordnung. Sind sowohl ID als auch Stream-Adresse mehrdeutig, bleiben die Sender ohne Programmdaten erhalten; für eine sichere Zuordnung müssen in der Quellplaylist eindeutige IDs ergänzt werden.

## Sender bearbeiten und Logos

Über **Bearbeiten** in einer Senderzeile lassen sich **Sendername** und **Kategorie** ändern. Kategorien können aus der Vorschlagsliste gewählt oder frei eingegeben werden; ein leeres Feld entfernt die Kategorie.

- Der neue Sendername erscheint im Webinterface, im Anzeigenamen und `tvg-name` der Zusatz-Playlist sowie im `<display-name>` des XMLTV-Exports.
- Die Kategorie wird als `group-title` in der Zusatz-Playlist ausgegeben und im Kategorienfilter angezeigt. Sie verändert nicht die Genres der EPG-Sendungen.
- Änderungen werden dauerhaft anhand der bestehenden Senderidentität gespeichert. Sender-IDs, Stream-Adressen und EPG-Zuordnungen bleiben erhalten. Die Quelldatei wird nicht geändert.
- Bei neuen Imports bleiben angepasste Felder erhalten. Nicht angepasste Felder folgen der Quelle. **Quellwerte übernehmen** entfernt die Anpassungen und stellt die aktuellen Quellwerte wieder her.
- Doppelte Anführungszeichen und Steuerzeichen sind in den bearbeiteten Feldern nicht zulässig; die maximale Länge beträgt 200 Zeichen.

Die Senderliste und der Bearbeitungsdialog zeigen das Senderlogo aus der Playlist. Fehlt dort eine Logo-Adresse, wird das Logo des zugeordneten EPG-Senders verwendet. Die Vorschau lädt HTTP-/HTTPS-Bilder über den EPG-Dienst, damit der Browser die internen Logo-Adressen nicht selbst erreichen muss. Bei fehlenden oder nicht erreichbaren Logos erscheinen Initialen als Platzhalter; der Hinweis beim Darüberfahren zeigt den Fehlerzustand.

Für die Vorschau muss der EPG-Dienst die Logo-Adresse erreichen können. Die Logo-Adressen in den exportierten Dateien bleiben unverändert; Kodi lädt seine Logos weiterhin selbst. Für angepasste Sendernamen und Kategorien in Kodi die erzeugte `/playlist.m3u8` verwenden und dort neu laden.

## Aktualisierung und Speicherung

- Quellen werden beim Start, alle sechs Stunden und über **Jetzt aktualisieren** geladen.
- Neue Zuordnungen erzeugen sofort eine neue Ausgabe aus den zuletzt erfolgreich geladenen Quellen.
- Während einer Verarbeitung bleiben die bisherigen Dateien abrufbar. Überlappende Änderungen werden abgewiesen.
- Fehlerhafte Downloads oder ungültige Dateien ersetzen die letzte gültige Ausgabe nicht. Der Fehler erscheint im Webinterface und im Dienstprotokoll.
- Neue Quellen-URLs bleiben auch nach einem fehlgeschlagenen Import gespeichert; bis zu einem erfolgreichen Import gelten die bisherigen Quelldaten.
- Einstellungen, Zuordnungen, Quelldateien und Ausgaben liegen im Datenverzeichnis. `state.json` verweist auf die aktuelle unveränderliche Generation. Alte Generationen werden nach erfolgreichem Austausch entfernt.
- Nur einen Prozess pro Datenverzeichnis betreiben. Für eine konsistente Sicherung den Dienst kurz stoppen und das gesamte Datenverzeichnis kopieren.

Optionen:

```text
-config config.json        Konfigurationsdatei (Standarddatei optional)
-listen ADRESSE            HTTP-Adresse; überschreibt den konfigurierten Port
-data-dir ./data           Persistente Daten
-refresh-interval 6h       Aktualisierungsintervall
-epg-url URL               Gespeicherte EPG-URL beim Start überschreiben
-playlist-url URL          Gespeicherte Playlist-URL beim Start überschreiben
-playlist-file PFAD        Lokale Playlist verwenden (anstelle von -playlist-url)
```

Die Quellen-Flags sind optional. Für dauerhafte Änderungen über das Webinterface diese Flags beim regulären Dienststart weglassen, da sie sonst nach jedem Neustart erneut überschreiben. `-playlist-url` wechselt zurück zur URL-Quelle; `-playlist-file` aktiviert die lokale Datei. Beide gleichzeitig sind nicht erlaubt. Beispiel für die mitgelieferte Datei: `./epggenerator -playlist-file ./tv.m3u8`.

Unterstützt werden UTF-8-M3U/M3U8-Senderlisten mit `#EXTM3U` und `#EXTINF` sowie XMLTV in UTF-8. HLS-Segmentlisten sind keine Senderlisten. Größenlimits: 20 MiB für Playlists und 512 MiB für XMLTV, Download-Zeitlimit drei Minuten je Quelle. HTTP-Komprimierung wird automatisch verarbeitet; direkt angebotene `.gz`-Archive werden nicht unterstützt.

## Linux-Dienst mit systemd

Nach dem Bauen das Programm installieren und die Beispiel-Unit aus `deploy/epggenerator.service` verwenden:

```bash
sudo install -Dm755 epggenerator /opt/epggenerator/epggenerator
sudo install -Dm644 config.json /etc/epggenerator/config.json
sudo install -m644 deploy/epggenerator.service /etc/systemd/system/epggenerator.service
sudo systemctl daemon-reload
sudo systemctl enable --now epggenerator
journalctl -u epggenerator -f
```

systemd erstellt mit `DynamicUser` einen Dienstnutzer und mit `StateDirectory` das persistente Verzeichnis `/var/lib/epggenerator`. Den Port in `/etc/epggenerator/config.json` ändern und mit `sudo systemctl restart epggenerator` übernehmen. Bei einem Update nur das Programm ersetzen und den Dienst neu starten; die vorhandene Konfiguration behalten. Für eine eingeschränkte Bind-Adresse oder ein anderes Intervall `ExecStart` in der Unit anpassen. Bei einer bereits installierten älteren Unit die neue Unit übernehmen und `sudo systemctl daemon-reload` ausführen, damit deren bisheriges `-listen :8080` die Konfiguration nicht überschreibt.

Bei einer lokalen Playlist muss der Dienstnutzer die Datei und ihre übergeordneten Verzeichnisse lesen beziehungsweise betreten können. Die systemd-Beispielunit schützt Home-Verzeichnisse mit `ProtectHome=yes`; für diesen Betrieb die Playlist beispielsweise unter `/var/lib/epggenerator/tv.m3u8` ablegen und für den Dienstnutzer lesbar machen.

## API

| Methode | Pfad | Bedeutung |
| --- | --- | --- |
| GET | `/api/state` | Einstellungen, Sender, Vorschläge, Status und Zeitpunkte |
| PUT | `/api/settings` | `{ "epgUrl": "https://…", "playlistUrl": "http://…" }` speichern und Import starten |
| POST | `/api/refresh` | Quellen aktualisieren, Body `{}` |
| PATCH | `/api/mappings` | `{ "mappings": { "SENDER-SCHLÜSSEL": "EPG-ID" } }`; leere EPG-ID hebt Zuordnung auf |
| PATCH | `/api/channels` | `{ "channels": { "SENDER-SCHLÜSSEL": { "name": "Sendername", "group": "Kategorie" } } }`; `null` statt des Objekts setzt beide Felder auf die Quelle zurück |
| GET | `/api/logo?key=SENDER-SCHLÜSSEL` | Logo-Vorschau eines importierten Senders; Schlüssel URL-kodieren |
| GET | `/epg.xml` | Aktuelle XMLTV-Ausgabe, optional `?download=1` |
| GET | `/playlist.m3u8` | Zusatz-Playlist, optional `?download=1` |

Schreibende Anfragen benötigen `Content-Type: application/json`. Sie antworten mit `202 Accepted`, sobald die Verarbeitung gestartet wurde; `/api/state` meldet mit `busy`, `lastError` und `lastGenerated` das Ergebnis. Während einer laufenden Verarbeitung wird `409 Conflict` zurückgegeben. Vor dem ersten erfolgreichen Import liefern die Ausgabe-URLs `503`.

Für eine lokale Playlist bei `/api/settings` zusätzlich `"playlistFile": "/pfad/zur/tv.m3u8"` angeben. Ein nichtleerer Dateipfad aktiviert die lokale Quelle; `playlistUrl` wird dann nicht verwendet. Mit `"playlistFile": ""` oder ohne dieses Feld wird die URL-Quelle verwendet. Bestehende Einstellungen und API-Anfragen ohne `playlistFile` funktionieren unverändert.

## Prüfen

```bash
go test ./...
go test -race ./...
go vet ./...
node --check web/app.js
```

Wenn `tv.m3u8` und `epg-de.xml` im Projektverzeichnis liegen, prüft `TestProvidedFiles` zusätzlich die bereitgestellten Beispieldaten: 54 Playlist-Sender, 438 EPG-Sender, 78.820 Sendungen, 38 exakte ID-Treffer und 15 fehlende Playlist-IDs. Ohne diese Dateien wird der Test übersprungen. Bei anderen Beispieldateien den Test gezielt anpassen oder einzelne Tests mit `go test -run TESTNAME ./...` auswählen.
