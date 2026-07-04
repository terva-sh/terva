# Web UI translations in the `terva locale` workflow

Status: **implemented** (all three phases shipped). Extends the i18n foundation
(docs/localization.md, `packages/i18n`), the `ctrlproto` wire, and the Preact
client under `packages/agent/web/`. Builds on the PWA i18n already shipped
(server advertises its locale in the hello; the client renders through an
English-as-key `t()`/`tn()` layer). Operator-facing docs: docs/web.md
("Translating the web panel").

## Motivation

A regular terva user who wants the UI in their own language already has a real
authoring loop for everything *except* the web panel:

```
terva locale init <lang>     # seed $TERVA_HOME/locales/<lang>.json with English keys
# … fill them in, aided by gap-capture into <lang>.todo.json as you use terva …
terva locale merge <lang>    # fold filled-in todo entries into <lang>.json
terva locale export <lang>   # bundle it up to submit a PR for a future release
```

The web client is the one hole. Its strings are **bundled into the PWA**
(`client/src/locales/<lang>.json`, baked into `client/dist`), so they are
invisible to every stage of that loop: `terva locale init` doesn't seed them,
gap-capture never sees them, `export` doesn't include them, and — critically —
the `$TERVA_HOME/locales` **overlay cannot reach them**, so a translator can't
edit a web string and reload the panel to check it.

Goal: make the web UI's strings first-class citizens of the same workflow, so a
user localizes the whole product — TUI, CLI help, prompts, *and* the web panel —
through one set of tools, and a browser reload shows their edits.

## The three stages, and where the gap is

Any i18n system separates **extraction** (find the keys), **translation** (fill
catalogs per language), and **resolution** (swap at runtime). terva does all
three for its Go code: `terva-i18n-lint` extracts → `terva locale` + gap-capture
translate → `i18n.T` resolves. The web client today does only resolution, against
a bundle. Closing the gap means wiring the web strings into extraction and
translation too, and letting the client resolve against a *server-served* catalog
instead of a frozen bundle.

## Design

### 1. A `web` catalog — separate file, main-UI semantics

Add a fourth catalog, `web`, laid out like `prompts/` and `help/` (its own file
set, embedded default + `$TERVA_HOME` overlay):

```
packages/i18n/locales/web/en.json     # reference (extracted from the client)
packages/i18n/locales/web/<lang>.json # shipped translations
$TERVA_HOME/locales/web/<lang>.json   # operator overlay (layered per-key)
```

But **not** `prompts`/`help` *semantics*. Those are dotted-key, singular-only
catalogs (`"help.locale"` → one template) because they hold large stable blocks
that never pluralize. The web UI is English-as-key **and pluralizes**
(`"%d tool call"` / `"%d tool calls"` via CLDR). So `web` is modelled on the
**main UI catalog** — English-as-key, singular *and* plural — just located in its
own file.

This is the one structural change to `packages/i18n`: today there is exactly one
English-as-key singular+plural catalog (the root `<lang>.json`); this generalizes
the catalog to carry a second, named `web`, loaded and overlaid by the same
`merge` path. `i18n.T`/`TN` keep resolving the root catalog unchanged (the server
never resolves web strings itself — see §3); `web` is loaded purely to be served
and to be authored via `terva locale`.

Why separate rather than folded into `<lang>.json`: sole ownership of each
reference file (the Go extractor owns `en.json`; the new client extractor owns
`web/en.json` — they never fight over one file), a self-contained client catalog
(~70 keys served, not the whole 610-key UI catalog), and it matches the multi-file
shape translators already work with. The cost is that a handful of strings shared
with server-side UI (e.g. "Usage", "Settings") get translated in both files;
acceptable, and most web strings are client-unique anyway.

### 2. Extraction — a client-side twin of `terva-i18n-lint`

The Go lint can't see TypeScript, so a small companion tool extracts the client
keys. It walks `client/src` with the TypeScript compiler API (already a dev
dependency), finds `t("…")` / `tn(n, "…", "…")` calls with literal string
arguments, and writes `packages/i18n/locales/web/en.json` (singular strings and
`{one,other}` plural objects — the Go locale JSON shape). Like the Go tool it has
a `-check` mode that fails when the reference is stale, wired into CI (`just
web-build` / the i18n gate). Non-literal `t(someVar)` calls fail loudly, matching
the Go extractor's hard-fail stance, so a string can't silently escape the
catalog.

### 3. `terva locale` learns `web`

`terva locale` already iterates a catalog registry (`i18n.KeyedCatalogs()`); it
gains awareness of `web` for `init` (seed `web/<lang>.json` from the reference),
`validate`, `diff`/`status` (coverage %), `merge` (fold `web/<lang>.todo.json`),
and `export`. Because `web` is English-as-key singular+plural (not dotted-key),
it's handled on the main-UI-catalog code path, not the keyed one. After this, the
init → fill → merge → export → PR loop covers the web panel with no special steps.

### 4. Serving + reload

The server never renders web strings (the client does), so it only needs to hand
the client the effective catalog:

- New `ctrlproto` session method `i18n.catalog {lang}` → the merged
  (`embedded ⊕ $TERVA_HOME overlay`) `web` catalog for the active language, read
  **fresh** so an overlay edit is picked up without a daemon restart.
- The client fetches it on connect and merges it over its bundled base (the
  bundle, a build-time copy of the shipped `web/<lang>.json`, remains only for
  offline / first-paint; the server copy wins).
- **Reload = a browser refresh.** Reconnecting re-fetches the web catalog and
  re-runs `i18n.Configure` (re-reading every overlay), so both client-owned
  strings *and* server-rendered titles/labels reflect the edit, then the client
  re-renders. No dedicated button, though one is trivial to add.

### 5. Client resolution (already shipped, minor change)

`client/src/i18n.ts` already resolves English-as-key with `Intl.PluralRules`. The
only change is that `setLocale` accepts the server-fetched catalog (overlay
included) rather than only the bundled one — `t()`/`tn()` are untouched.

## Non-goals / deferred

- **Runtime gap-capture for web keys — explicitly deferred until a concrete need
  appears.** (The client would report `t()`/`tn()` misses back over ctrlproto so
  they land in `web/<lang>.todo.json` as you browse, the way `TERVA_CAPTURE_LOCALE`
  does for the Go catalogs.) It buys almost nothing here: the build-time extractor
  already seeds *every* web key at `init` and `diff` reports exact coverage, so
  nothing is hidden; and because the extractor hard-fails on non-literal `t()`
  args, there are no dynamic strings for runtime capture to catch that static
  extraction misses. Its only real value is usage-ordered prioritization and
  parity with the TUI's capture rhythm — against real plumbing (a client→server
  miss channel + feeding i18n's capture). Revisit only if the translate-as-you-go
  cadence is actually wanted for the web panel; until then, `init` + edit + reload
  is the loop.
- **A file-watcher** that auto-pushes on overlay edit. A browser refresh is enough
  for an authoring loop; a watcher is a later nicety.
- **Multi-tenant / per-connection locale.** Single active language for the daemon,
  as today (see the shipped hello-locale mechanism).

## Phasing

1. `web` catalog in `packages/i18n` (load + overlay + serve accessor) and the
   client-side extractor + its reference `web/en.json`; move the existing bundled
   Finnish into `web/fi.json`.
2. `ctrlproto` `i18n.catalog` + client fetch/merge/reload.
3. `terva locale` support for `web` (init/validate/diff/merge/export) + CI gate.

Each phase is independently landable and `just ci`-green.
