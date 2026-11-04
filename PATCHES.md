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

## Pflege
Upstream regelmäßig in `main` mergen. Einen PR aus der Liste nehmen, sobald er upstream gemergt ist. Images entstehen über Tags der Form `v0.31.0-nas.N`.
