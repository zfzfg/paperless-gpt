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

## Pflege
Upstream regelmäßig in `main` mergen. Einen PR aus der Liste nehmen, sobald er upstream gemergt ist. Images entstehen über Tags der Form `v0.31.0-nas.N`.
