# The terva design system

terva draws three web surfaces: the control panel (`terva web`), Stage, and
the terva.sh landing page. This page explains where their colours come from,
how to change one, and which tests hold them together.

## One source

`assets/brand/tokens.json` holds every colour. It has three sections.

| Section | What it holds | Who reads it |
|---|---|---|
| `brand` | The brand pigments: Tar Black, Birch White, Resin Amber, and the rest | The landing page and `assets/brand/README.md` keep hand-written copies |
| `panel` | The control panel's light and dark palettes, and its scheme-independent intent colours | The generated stylesheet |
| `stage` | Stage's presets. Dusk is the base, and Parchment, Nocturne and Rose override part of it | The generated stylesheet |

The web client does not read the JSON at run time. A renderer turns it into
`packages/agent/web/client/src/ui/tokens.css`, which is committed. Both apps
import that file before any other sheet.

## Three layers

1. **Tokens.** The literal values, in `tokens.css`. A component rule names a
   token, and never repeats its value.
2. **The shared contract.** `ui/ui.css` styles the components that both apps
   share. It reads only `--ui-*` names, such as `--ui-bg`, `--ui-accent` and
   `--ui-warn`.
3. **The app mapping.** Each app maps the `--ui-*` names onto its own tokens,
   in `styles.css` for the panel and `stage.css` for Stage. The panel also
   points its working names (`--bg`, `--fg`) at the light or the dark arm.
   The scheme switch moves those pointers, and never restates a colour.

Density, type sizes and radius stay in each app sheet. They describe how an
app is laid out, not which colour it is.

## Themes

The panel has a light and a dark scheme. A person picks light, dark, or the
system setting, and `src/scheme.ts` writes the choice to `data-scheme` on the
document. With no choice, `prefers-color-scheme` decides.

Stage has four presets. `src/apps/stage/theme.ts` writes the choice to
`data-theme`. Parchment is the one light preset. A preset declares only the
roles it changes, and inherits the rest from Dusk. The renderer refuses a
preset role that Dusk does not declare, because that role would have no value
in the other presets.

The landing page is dark only.

## Change a colour

1. Edit `assets/brand/tokens.json`.
2. Run `just design-tokens`. It rewrites `tokens.css`.
3. Run `just web-build`, because the built client in `dist/` is committed.
4. For a brand colour, also update the landing page and the palette table in
   `assets/brand/README.md`.
5. Commit everything together.

To add a role, add it to the JSON first. Then map it in the app sheet that
uses it. A panel role needs a light and a dark value.

## The guards

| Test | What it holds |
|---|---|
| `src/ui/tokens/tokens.test.ts` | `tokens.css` is exactly the rendering of `tokens.json`. The renderer refuses a malformed table. |
| `src/ui/ui-conformance.test.ts` | Each app maps the whole `--ui-*` contract. No custom property refers to itself. Each property that a rule reads is declared, whether or not the read has a fallback. |
| `src/ui/contrast.test.ts` | Each text colour meets 4.5:1 on its ground, in every scheme and preset. Some pairs fail today, and a list records them. The list can only get shorter. |
| `src/scheme.test.ts`, `src/apps/stage/theme.test.ts` | The scheme switch re-maps tokens, and no rule copies a palette literal. |
| `packages/testsupport/brand_tokens_test.go` | The README palette table and the landing page agree with `tokens.json`. |

The vitest suites run in CI with the rest of the web client tests. The Go test
runs with `go test ./packages/testsupport`.

## Not covered yet

- The terminal UI has its own palette in `packages/tui/theme.go`, with themes
  described in [themes.md](themes.md). It does not read `tokens.json` yet.
- A few component rules in `stage.css` still carry literal colours that are
  not in the palette.
- The contrast baseline lists the pairs that still need new colours.
