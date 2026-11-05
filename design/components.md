# Orbit UI 2 · Komponenten

Design-System der Oberfläche. Tokens: `tokens-light.json`, `tokens-dark.json`
(Kontrast geprüft, 0 Verstöße gegen WCAG AA). Umsetzung: `dist/ui.css`, nur
aktiv bei `<html data-ui="2">` (Standard). Die klassische Oberfläche kommt im
Benutzermenü über „Klassische Oberfläche“ zurück. Musterseite mit allen
Bausteinen: Suche → „Design-System“.

Prüfen nach jeder Farbänderung:

```bash
python3 contrast.py design/tokens-light.json
python3 contrast.py design/tokens-dark.json
```

## Grundregeln

- Farben nur über Rollen (`--c-*`), nie als Hex im Komponenten-CSS.
- Höhen: Steuerelemente 34 px (md), Chips/Filter 28 px (sm), Tabellenzeilen 44 px.
- Abstände: 4er-Raster (4, 8, 12, 16, 20, 24, 32).
- Radius: 4 px Badges, 6 px Steuerelemente, 8 px Karten, 10 px Dialoge.
- Schrift: Inter, Basis 13,5 px; Titel 18 px/650, Abschnitte 15 px/650.
- Orange (`accent-bg` #c2410c) nur für die eine Hauptaktion pro Bereich, Links und den aktiven Zustand.

## Bausteine

```
Button
  variants  primary, standard, ghost, icon, text, danger
  size      34 px (md); in Menüs 32 px
  states    default, hover, active, focus-visible (2 px Ring accent), disabled (50 %)
  tokens    primary: bg accent-bg, text on-accent (5,18:1), hover accent-bg-hover
  a11y      echtes <button>, Icon-Buttons mit aria-label

Eingabefeld / Auswahl / Textbereich
  size      34 px, Textbereich frei
  states    default, hover, focus (Rand focus + 3 px Halo), invalid (Rand danger), disabled
  tokens    border border-input (3,27:1 UI-Kontrast), bg surface, placeholder text-subtle
  a11y      <label for>, Fehlertext mit role=alert im Formular

Chip (Filter)
  size      28 px, Pille
  states    default, selected (accent-soft / accent-text), hover
  a11y      aria-pressed

Status-Badge
  variants  ok (success), warn, fail (danger), neutral, maint (info)
  size      22 px, 11,5 px/600, Punkt in currentColor
  a11y      Text immer sichtbar, Farbe nie allein

Tag
  20 px, neutraler Grund, farbiger Punkt pro Tag (Farbton aus dem Namen)

Panel / Karte
  surface, Rand border, Radius 8, Kopf 48 px mit Titel 15 px und Aktionen rechts

Tabelle
  Kopf 36 px, Großbuchstaben 11,5 px, Hintergrund surface-2, sticky
  Zeile 44 px, Hover surface-2, ausgewählt accent-soft, Zahlen rechtsbündig tabellarisch

Tabs
  Unterstreichung 2 px accent auf aktiver Lasche, 40 px hoch, horizontal scrollbar

Dialog
  Kopf (Kicker 11,5 px, Titel 18 px), Inhalt 20–24 px Rand, Fußleiste surface-2
  mit Abbrechen links und Hauptaktion rechts; Esc schließt; Fokus bleibt im Dialog

Hinweis
  info (Standard), warn, error; linker Farbbalken 3 px; leere Fehler werden ausgeblendet

Aktionsmenü
  <details>; bleibt bei Live-Aktualisierung offen; Einträge 32 px, gefährliche rot

Leerer Zustand
  Symbol 8 px Radius neutral-soft, Titel 15 px, Satz, optional eine Hauptaktion
```

## Seitenrahmen

- Kopfleiste 52 px dunkel (`nav`): App-Umschalter, Bereichsmenü (Prism Element), Suche, Alarme, Aufgaben, Benutzer.
- Seitenkopf: Titel, darunter Beschreibung, Aktionen rechts; mobil darunter.
- Inhalt mit 24 px Rand (mobil 12 px), Aufgabenleiste unten 32 px.
