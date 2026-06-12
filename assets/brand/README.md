# Terva brand assets

`terva-logo.svg` is the **source of truth** for the Terva logo. It is a
hand-authored vector reconstruction of the approved icon render (an AI-built
PNG, since retired), regularized to exact geometry and the official palette.
Derive all logo sizes/formats from the SVG — `terva-logo.png` here is a
1024×1024 export of it (rendered with resvg).

## Logo geometry (1024×1024 viewBox)

- Design center `(512, 496)` — shared by the hexagon, asterisk, and side gaps.
- **Harness shell**: regular pointy-top hexagon, outer apothem 300, stroke 76,
  split into four brackets by a 40-wide slit at the top vertex, 38-tall slits
  at the mid-sides, and bottom end-cuts running parallel to the trapezoid
  sides (36 horizontal clearance).
- **Wildcard asterisk**: three 48×218 bars crossing at the center, rotated
  0°/+60°/−60°.
- **Footer block**: trapezoid from y 702 to 822, half-widths 72 (top) and
  132 (bottom) — sides slope exactly 2:1; corner radius 4 top, 10 bottom.

The original AI render's hexagon was slightly vertically stretched with
inconsistent edge angles; the SVG regularizes it to true 30°/60° geometry
(verified ≥94% per-element pixel overlap with the render everywhere else).

## Lockup (`terva-lockup.svg`)

Horizontal lockup: mark + "Terva" wordmark + rule + tagline. The tagline is
**"Harness the wildcard"** (sentence case on the lockup; "the Wildcard" may
be capitalized in prose). `terva-lockup.png` is a 1364×510 resvg export
of it. Vectorized June 2026 from an approved AI render, regularized like
the icon:

- **Mark**: the canonical icon geometry, inlined verbatim under a
  `translate(100 190) scale(0.54)` transform — keep it in sync with
  `terva-logo.svg` if the master ever changes.
- **Wordmark**: five hand-drawn glyphs on a shared grid — baseline 552,
  cap height 218 (top 334), x-height 159 (top 393), stroke 30, 45°
  chamfers, 16–18 unit gaps echoing the shell segments (floating T
  crossbar, slit 'e' ring, detached 'r' arm, truncated 'v', segmented
  'a' ring with midbar).
- **Rule**: 5-unit stroke with a 45° lead-in under the T, ending in a
  54×12 Footer Amber cursor dash right-aligned with the 'a'.
- **Tagline**: JetBrains Mono Regular outlined to paths at 48 units,
  tracking as the font ships (advance 28.8).
- **Grays**: rule and tagline use `#7A7979` (not yet a named palette
  slot; Slate is too dark on black).

## Palette

| Role                  |       Hex | Use                                      |
| --------------------- | --------: | ---------------------------------------- |
| **Tar Black**         | `#11100E` | Primary dark background                  |
| **Deep Tar**          | `#0A0A09` | App icon background / extra-dark version |
| **Charcoal**          | `#1B1D1E` | Secondary dark UI surface                |
| **Birch White**       | `#F5F1E8` | Harness shell / light foreground         |
| **Resin Amber**       | `#D9902F` | Primary brand amber                      |
| **Deep Resin Orange** | `#F29100` | Center asterisk / wildcard               |
| **Hot Amber**         | `#F2B84B` | Highlight amber                          |
| **Footer Amber**      | `#FDB515` | Tar/cursor/footer block                  |
| **Pine Green**        | `#1F4D3A` | Optional secondary accent                |
| **Terminal Mint**     | `#6EE7B7` | Optional terminal/tech accent            |
| **Slate**             | `#2E343B` | Neutral UI gray                          |

Logo color mapping:

| Element         | Color                            |
| --------------- | -------------------------------- |
| Background      | Deep Tar `#0A0A09`               |
| Harness shell   | Birch White `#F5F1E8`            |
| Center wildcard | Deep Resin Orange `#F29100`      |
| Footer block    | Footer Amber `#FDB515`           |

Never brand as "Terva AI" — the product is **Terva**.

## Exports (`exports/`)

Generated rasters from the June 2026 asset pack: square
(`terva-logo-N.png`, dark background — the 1024 lives at the top
level as `terva-logo.png`, the resvg render documented above),
transparent
(`terva-logo-transparent-N.png`), rounded-square app icons
(`terva-app-icon-N.png`), and the favicon/touch set (`favicon.ico`,
`apple-touch-icon.png`, `android-chrome-*.png`). Regenerate from the
SVGs when the master changes.

`favicon.svg` is a copy of the master (same artwork, dark
background). `terva-logo-transparent.svg` is the master minus the
background rect — regenerate it by deleting that one line.

## Where the brand ships

- `packages/provider/auth/assets/terva-logo.png` — the 512px square,
  embedded in the binary and served at `/logo.png` on the OAuth
  callback pages.
- `README.md` header — `exports/terva-logo-256.png` (the square
  version: the cream harness needs its dark background on light
  pages).
- `docs/vanity/site/` — favicon set + touch icons + webmanifest at the
  root, plus the page asset pack in `assets/` (lockup + logo, SVG/PNG,
  dark and transparent) for terva.sh (pushed to the
  terva-sh/terva-sh.github.io repo).
