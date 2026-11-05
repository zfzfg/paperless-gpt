# Patches des Forks

Der Branch `main` dieses Forks ist `icereed/paperless-gpt` plus diese offenen Upstream-PRs, in dieser Reihenfolge gemergt, und die eigenen Änderungen unten.

Basis: `1d07ced` (v0.31.0 plus 3 Commits)

| PR | Commit | Grund |
|---|---|---|
| [#1224](https://github.com/icereed/paperless-gpt/pull/1224) | `e61e6ca` | Image-Bau scheitert sonst am `musl-dev`-Pin |
| [#1217](https://github.com/icereed/paperless-gpt/pull/1217) | `b9fe010` | Kaputte PDFs bringen das Programm nicht mehr zum Absturz |
| [#1229](https://github.com/icereed/paperless-gpt/pull/1229) | `08fffdd` | Nicht erreichbarer Anbieter zählt nicht als Fehlversuch |
| [#1218](https://github.com/icereed/paperless-gpt/pull/1218) | `c60b668` | Bestehende Custom Fields werden korrekt überschrieben |
| [#1206](https://github.com/icereed/paperless-gpt/pull/1206) | `a77af08` | Auto-OCR behält Tag-Änderungen aus Paperless |
| [#1208](https://github.com/icereed/paperless-gpt/pull/1208) | `2975df4` | Tag- und Workflow-Lauf behält Tag-Änderungen, baut auf #1206 auf |

## Eigene Änderungen
- `ocr/mistral_provider.go`: OCR-Anfragen werden bei HTTP 429 und 5xx bis zu 5-mal wiederholt, `Retry-After` wird beachtet.
- `ocr/mistral_provider.go`: Bild-Platzhalter wie `![img-0.jpeg](img-0.jpeg)` werden aus dem OCR-Text entfernt.
- `mistral_llm.go`: Eigener Chat-Client für `LLM_PROVIDER: mistral`. Der Client aus langchaingo kann Antworten mit `content` als Array nicht lesen. Eine Antwort ohne Text oder eine abgeschnittene Antwort ist jetzt ein Fehler.
- `app_llm.go`, `created_date_prompt.tmpl`: Ohne vollständiges Datum antwortet das Modell `unknown`. Ein Datum nach dem Hinzufügen sowie „heute“ gegen ein anderes vorhandenes Datum werden verworfen.
- `background.go`, `unreachable.go`: Wartezeit nach Fehlern höchstens 5 Minuten und abbrechbar, ein OCR-Fehler stoppt den Tag-Lauf nicht mehr. HTTP 401, 402, 403, 429 und 5xx gelten mit Grenze 48 statt 3 als Störung des Anbieters.
- `llm_client.go`: Dauerhafte 4xx-Fehler werden nicht wiederholt.
- `app_llm.go`, `paperless.go`, `normalize_monetary.go`: JSON in Fließtext wird erkannt. Eine unbrauchbare Custom-Field-Antwort setzt das Fehler-Tag, die übrigen Felder bleiben. Leere Werte werden übersprungen, Zahlen für Textfelder zu Text umgewandelt.
- `ocr.go`, `ocr_runs.go`, `background.go`: Automatische Läufe verwenden 24 Stunden lang die Seiten eines fehlgeschlagenen Laufs wieder und überspringen OCR, wenn sein Text bereits im Dokument steht.
- `ocr/mistral_provider.go`: Kein Upload an die Files-API mehr. Eine PDF, die den Provider erreicht, ist ein dauerhafter Dokumentfehler ohne Netzwerkaufruf. `mistral_ocr` nimmt nur den Modus `image`. `pdf` und `whole_pdf` werden beim Start über die Umgebung abgelehnt, beim Speichern der Standardwerte, beim Lauf und beim Speichern eines Workflows mit OCR. Ein schon gespeichertes `whole_pdf` in `config/settings.json` startet das Programm trotzdem, damit man es in der Oberfläche zurücksetzen kann, und jeder Lauf scheitert, bis der Modus `image` ist. Der Modus wird nicht still auf `image` umgeschrieben. Seitenbilder gehen als `image_url` mit Base64 im JSON mit, nur JPEG, PNG, GIF, WebP und AVIF. Ein rohes TIFF wird nicht gesendet.
- `ocr.go`, `page_routing.go`: `PDF_SKIP_EXISTING_OCR` gilt auch im Modus `image`, für PDFs, und damit auch im Workflow-Pfad, weil der `ProcessDocumentOCR` ruft. Geprüft wird das Original (`?original=true`), nicht die Archivversion. Das ist auch bei `PAPERLESS_OCR_MODE=off` richtig: Paperless übernimmt dann die vorhandene Textebene in `document.content` und legt keine Tesseract-Ebene darüber. Übersprungen wird nur, wenn jede Seite eine klare digitale Textebene ist (`clearVerdict`: Bildanteil unter 0,2, Buchstabenanteil mindestens 0,6, mindestens 20 Wörter, kein unsichtbarer Scanner-OCR-Text) und der Paperless-Inhalt mindestens 30 Zeichen ohne Leerzeichen hat (`minReusableContentChars`, dieselbe Grenze wie „keine Textebene“). Eine graue Seite („brauchbare Textebene“), eine Bildseite, eine Scanner-OCR-Ebene, eine gemischte PDF oder eine dünne oder leere Inhaltsangabe führen zur OCR des ganzen Dokuments. Schlägt der Download oder die Analyse fehl, läuft die OCR weiter, das Dokument scheitert nicht an der Prüfung. `pdfocr.DetectOCR` taugt dafür nicht: es sucht nur eine optionale Ebene mit dem Namen „OCR Text“ und sieht eine normale Textebene nicht. Eine einzige solche Ebene würde außerdem das ganze Dokument überspringen, auch gemischte Seiten. Das weicht vom bisherigen Verhalten in `pdf` und `whole_pdf` ab, die genau diese Namensprüfung genutzt haben. Andere Anbieter dürfen diese Modi weiter nutzen und bekommen dieselbe Seitenprüfung.
- `app_llm.go`: `TAG_EXCLUDE_LIST`, kommagetrennt, ohne Groß- und Kleinschreibung. Die Namen stehen nicht im Prompt, und eine Modellantwort, die sie trotzdem nennt, wird verworfen. Das gilt für manuelle Vorschläge und Workflows, weil beide `getSuggestedTags` nutzen. Vorhandene Tags des Dokuments bleiben, auch wenn sie auf der Liste stehen. Tags, die das Programm selbst setzt (`AddTags`, Abschluss-Tag), gehen nicht durch diese Prüfung. Trigger- und Abschluss-Tags der Workflows sind schon Systemtags. `CREATE_NEW_TAGS` bleibt standardmäßig aus, ein unbekannter Name aus der Antwort wird dann verworfen. Die Liste ist nicht im Code festgeschrieben.
- `paperless.go`: Mehrseitige TIFF-Dateien und GIFs mit mehr als einem Frame werden abgelehnt, in `DownloadDocumentAsImages` und im Bildzweig von `DownloadDocumentAsPDF`. `imaging.Decode` und der Standard-TIFF-Decoder liefern nur die erste Seite, der Rest ginge sonst verloren. Eine einzelne TIFF-Seite wird zu JPEG und geht als `image/jpeg` an Mistral. BigTIFF und eine nicht lesbare Seitenanzahl sind ebenfalls ein Fehler. Animiertes WebP wird nicht erkannt. Der Vision-Pfad `ocr/llm_provider.go` schickt bei `VISION_LLM_PROVIDER=mistral` Seiten als `data:image/jpeg;base64` im Chat, nicht über die Files-API. Das ist ein anderer Weg als `mistral_ocr` und wurde nicht umgebaut.
- `persistence.go`: `onMount` bricht ab, wenn `filepath.Dir` nicht mehr aufsteigt. Auf Windows bleibt `Dir` an `C:\` stehen, der Lauf wäre sonst endlos. Unter Linux ändert das nichts, `/` zählt weiter nicht als Mount.

## Pflege
Upstream regelmäßig in `main` mergen. Einen PR aus der Liste nehmen, sobald er upstream gemergt ist. Images entstehen über Tags der Form `v0.31.0-nas.N`.
