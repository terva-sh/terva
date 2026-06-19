# terva and zot

terva is an **agent harness** — a permissioned loop where a model
drives tools. It ships wired for coding but isn't bounded by it (point
it at extensions or MCP servers and it operates anything they expose),
and it projects one hardened core through many front ends: terminal,
editor (over ACP), chat, and an embeddable RPC/SDK. It is a hard fork of
[zot](https://github.com/patriceckhart/zot) that took its own name once it
diverged too far to carry upstream's — not a replacement or successor for
zot, which lives on. *terva* is Finnish for pine tar — the traditional
preservative and cure-all.

**terva is not a replacement for zot.** zot continues upstream as its
own project; terva is its own project that weights priorities
differently. zot favors moving fast; terva bets on the *seams* —
hardening the contract surfaces and growing the test suite until the
core is safe to change — then spends that safety on widening the
surface: more front ends, more ways to plug in. The two share DNA, and
terva still pulls worthwhile upstream changes (see *Upstream posture*
below), but they are separate projects with separate goals.

## What's different

### Contracts over conventions

The seams between harness parts are typed and enforced instead of
hand-maintained:

- **One canonical event serializer** (`core.WireEvent`) feeds
  `--json` mode, the RPC server, the embedding SDK, and swarm replay.
  Golden tests pin the wire shape in core and at the compiled-binary
  level, so four consumers can't drift apart again.
- **Typed provider errors** replace substring classification of
  failure text, so retry/rescue behavior is decided on structured
  data.
- **Declarative model capabilities** — vision, reasoning, image
  generation — are tags on the model catalog with per-capability
  defaults and a single merge path, replacing hardcoded
  provider-name checks (`/model` filters on them with `:img`,
  `:reasoning`).
- **One chat-ops loop.** Chat services implement a small `Connector`
  contract (pure transport); pairing, queueing, chunking, and
  commands exist once, instead of being rewritten per service.
- **Capability structs, never interface probing** through wrapper
  chains — a bug class upstream hit (tool-image mirroring silently
  disabled by a wrapping client) that the convention exists to
  prevent.

### Many front ends, one core

The hardened core is projected through more surfaces than zot exposes,
each a thin client of the same agent loop and event stream rather than
a separate reimplementation: an **editor integration over ACP** (drive
terva from Zed and other ACP editors), the **chat connectors** below,
print/json for scripting, and an embeddable RPC/SDK. The tool surface
is general, too — built-in coding tools (read/write/edit/run) alongside
**MCP servers**, **pre/post tool-use hooks**, and **extensions in any
language** — so terva operates services, hardware, or your own systems,
not just files. All of it runs under one permission and policy model.

### Tests as the safety net

An end-to-end harness builds the real binary and drives print/json/
RPC modes against a fake provider. Golden frame tests pin both
subprocess wire protocols (extensions and connectors) byte-for-byte.
The chat loop, the external-connector proxy (including crash/restart
budgets and attachment containment), and the model-catalog layering
have dedicated suites. CI runs the race detector and a build-tag
matrix on every change.

### Bugs and oddities fixed along the way

Among them: a queue double-drain race in the chat bridge, tool-result
images silently dropped through client wrapper chains, sessions
bricked when a non-vision model met a screenshot in the transcript
(now: per-model capability, images dropped with a visible note), and
self-update paths that would have replaced a fork binary with
upstream's release.

### Connector extensibility

Chat connectors got the same treatment extensions already had:
out-of-process executables in any language, speaking a small
JSON-lines protocol over stdio with **negotiated versioning**, loud
crash/restart handling, and host-side pairing (the host never sees
service tokens; the connector never sees pairing policy). A Go SDK
makes a minimal connector ~50 lines. See
[docs/connectors.md](connectors.md).

## Upstream posture

We watch zot and pull changes when they're worth pulling. A
translation workflow exists exactly for that:
`scripts/rename-upstream.sh` maps upstream's naming (and module path)
onto terva's, and `just upstream-merge` rebuilds an
`upstream-translated` branch from `upstream/main`, translates it, and
merges the translation. Upstream consumption is occasional, not a
cadence — their direction may inform ours; it doesn't steer it.

## Compatibility promises

- **zot extensions are a supported surface, permanently.** The
  extension wire format is frozen by golden tests — field names
  (including `zot_version`) never take rename sweeps — so an
  extension written for either harness runs on terva unchanged.
- **If upstream grows a connector protocol**, the intent is to bridge
  it at the boundary (an adapter, not wire re-convergence) so those
  connectors work on either harness too.
- **Existing zot installs keep working across the rename**: the
  self-updater accepts either binary name, `ZOT_*` env vars are
  honored (deprecation window: two tagged releases), pre-rename data
  directories and `.zot/` project dirs are read until you opt out,
  and `.zotsession` files import forever.
- **Migration is opt-in, never forced.** `terva migrate` (or
  `/migrate` in the TUI) copies the zot data dir to the terva
  location without overwriting anything, offers to delete the
  original and to rename a project's `.zot/` to `.terva/`, and
  writes a `$TERVA_HOME/.zot-fallback-disabled` marker that turns
  off legacy-directory discovery from then on (delete the marker to
  re-enable it). `ZOT_*` env vars keep working through their
  deprecation window and `.zotsession` import stays forever — the
  marker gates config-file autoloading only.

The full rename engineering record — phases, deviations, the
enforcement tests — lives in
[docs/plans/rename-terva.md](plans/rename-terva.md). A quantitative
divergence measurement (commit/diff/test metrics against translated
upstream, with the method to re-run it) lives in
[docs/architecture/09-zot-divergence.md](architecture/09-zot-divergence.md).
