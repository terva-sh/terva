# terva and zot

terva is an **agent harness** — a permissioned loop where a model drives
tools. It ships wired for coding but isn't bounded by it (point it at
extensions or MCP servers and it operates anything they expose), and it
projects one hardened core through many front ends: terminal, browser,
editor (over ACP), chat, and an embeddable RPC/SDK.

It began in May 2026 as a hard fork of
[zot](https://github.com/patriceckhart/zot) and took its own name once it
diverged too far to carry upstream's. *terva* is Finnish for pine tar — the
traditional preservative and cure-all that sealed boats and kept them
seaworthy; its default agent persona is *Mieli*, Finnish for "mind" (a mind
in a preserved vessel).

**terva is not a replacement, successor, or rename of zot.** zot continues
upstream as its own project, with its own goals. This document is the
honest account of how the two relate now — which is: not much, beyond a
shared starting commit and a handful of compatibility promises we keep on
purpose.

## Where things actually stand

The fork is no longer a variant of zot. It is a different program that
happens to share an origin.

| | |
|---|---|
| fork-only history begins | 2026-05-29 |
| commits on the fork line | ~1,100 |
| Go sources | ~894 files / ~198k lines |
| test files | 445 |
| upstream commits merged since July 2026 | 0 |

For scale: the last apples-to-apples measurement against a translated
upstream tree (2026-06-12) put terva at 284 Go files and 111 test files,
against zot's 213 and 65. terva has roughly **tripled** since that snapshot,
and the test suite has **quadrupled**. The measurement has not been re-run
because the premise it rests on — that a translated upstream tree is a
meaningful diff base — stopped being true.

### What terva grew that zot does not have

Whole subsystems, not features bolted onto upstream's:

- **A control plane.** Every front end — TUI, web, chat bot, editor —
  drives the agent through one versioned protocol (`ctrlproto`) instead of
  reaching into the loop directly. This is the deepest structural break
  with upstream: there is no seam left to merge against. See
  [controllers.md](controllers.md).
- **A browser front end.** `terva web` is a first-class control panel, not
  a viewer — same sessions, same permissions, same event stream as the TUI.
  See [web.md](web.md).
- **Chat connectors.** Built-in Telegram and Discord bridges plus
  **external connectors in any language** over a versioned JSON protocol,
  with group admission, interactive approvals over chat, per-chat sessions,
  and threads. See [connectors.md](connectors.md).
- **Background subagents.** Fan work out to parallel *swarm* agents from
  inside a session.
- **RAATI** — a deliberation primitive where several models argue a
  decision to a recorded verdict. See [raati.md](raati.md).
- **Personas and immersive modes.** `--chat` and `--play` reframe the
  harness away from coding, fronted by a persona or a SillyTavern
  **character card**, with a keyword-triggered **lore** context engine and a
  director that can voice a declared cast. See [personas.md](personas.md).
- **Localization.** Translate the UI, or override terva's wording *and its
  model-facing prompts* in place, via per-key overlays. See
  [localization.md](localization.md).
- **Skills, hooks, themes, image generation, session replay, egress
  control, workspace trust, resource limits** — each with its own doc.
- **An editor integration over ACP**, print/json modes for scripting, and
  an embeddable RPC/SDK.

Under all of it: typed contracts where upstream has conventions. One
canonical event serializer (`core.WireEvent`) feeds `--json`, the RPC
server, the SDK, the web panel, and swarm replay, with golden tests pinning
the wire shape at the compiled-binary level. Typed provider errors replace
substring classification of failure text. Model capabilities are
declarative tags on the catalog, not hardcoded provider-name checks. Chat
services implement a small `Connector` contract, so pairing, queueing,
chunking, and commands exist once.

And a test suite that is the real product of the exercise: an end-to-end
harness that builds the actual binary and drives it against a fake
provider, golden frame tests pinning both subprocess wire protocols
byte-for-byte, a VT-emulator harness for the TUI, and CI running the race
detector across a build-tag matrix on every change.

### Bugs fixed along the way

Among the ones worth naming, because they were inherited: a queue
double-drain race in the chat bridge, tool-result images silently dropped
through client wrapper chains, sessions bricked when a non-vision model met
a screenshot in the transcript (now: per-model capability, images dropped
with a visible note), and self-update paths that would have replaced a fork
binary with upstream's release.

## Upstream posture

**We do not track zot.** Upstream tracking was retired in July 2026. There
is no `upstream` remote, no sync mirror, no drift cadence, no merge budget.
The fork has diverged past the point where pulling upstream commits is
meaningful — most fundamentally, every terva front end now drives the agent
through the control plane, a seam upstream doesn't have, so their patches
don't apply to our tree in any but the most peripheral files.

What we *do*, occasionally: **look**. If zot ships something clever, we read
it and decide on the merits whether terva wants that idea — and then
implement it our way, against our contracts. Inspiration, not a dependency.
`scripts/rename-upstream.sh` still translates upstream naming and module
paths onto terva's, so a one-off manual port stays possible if a specific
change ever justifies the effort. Nothing is scheduled.

Their direction may inform ours. It does not steer it.

## Compatibility promises

These are deliberate, and they are the only places where divergence is
capped — increasingly, the only thing the two projects share.

### Still true, indefinitely

- **Existing zot installs keep working.** The self-updater accepts either
  binary name, `ZOT_*` env vars are honored, pre-rename data directories
  and `.zot/` project dirs are read until you opt out, and `.zotsession`
  files import forever.
- **Migration is opt-in, never forced.** `terva migrate` (or `/migrate` in
  the TUI) copies the zot data dir to the terva location without
  overwriting anything, offers to delete the original and to rename a
  project's `.zot/` to `.terva/`, and writes a
  `$TERVA_HOME/.zot-fallback-disabled` marker that turns off
  legacy-directory discovery from then on (delete the marker to re-enable
  it). The marker gates config-file autoloading only — `ZOT_*` env vars and
  `.zotsession` import are unaffected.
- **The extension wire format never takes rename sweeps.** Field names —
  including `zot_version` — are frozen by golden tests. An extension
  written against the zot protocol as it stands today loads on terva
  unchanged.

### Capped, honestly

- **zot extension support is pinned at the current zot protocol.**
  Upstream's extension protocol is still evolving (their spontaneous
  `open_panel` frames were the change that made this concrete). We are not
  chasing it. terva implements the protocol as forked, plus terva's own
  additions to it; a zot extension that depends on newer upstream protocol
  features is not guaranteed to load. We will revisit periodically and lift
  anything worth having, but bidirectional extension parity is not a goal
  and not a promise.
- **zot connectors are not planned.** Earlier versions of this document
  said the intent was to bridge any future upstream connector protocol so
  connectors would run on either harness. That is no longer the plan.
  terva's connector protocol is its own — versioned, with an SDK, and far
  enough along that adapting to a hypothetical upstream design would cost
  more than it returns. Write connectors against
  [connectors.md](connectors.md).

The short version: **what you already have keeps working; what upstream
builds next is not something terva promises to run.**

## Records

Two engineering records back this document. Both live in the development
repository rather than the public tree, so they are named here, not linked:

- `docs/plans/archive/rename-terva.md` — the rename record: phases,
  deviations, and the tests that enforce the compatibility promises above.
- `docs/architecture/09-zot-divergence.md` — the June 2026 quantitative
  divergence measurement and its method. A historical snapshot: it predates
  the control plane, the web front end, and the end of upstream tracking.
