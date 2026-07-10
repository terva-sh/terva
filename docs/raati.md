# RAATI — the deliberation panel

RAATI (Finnish *raati*, "jury/panel") convenes **three agents with deliberately
different priors** on one decisive question and returns a tallied verdict with
the dissent attached:

- **YATA-1** (mirror) — truth: what does the evidence actually establish?
- **KUSANAGI-2** (sword) — decisiveness: what happens if we act, or don't?
- **MAGATAMA-3** (jewel) — benevolence: who bears the cost?

The panel deliberates **blind** first (no unit sees another's ballot), then
cross-examines (each unit sees the others' verdicts and rationales and may
revise), then the ballots are tallied under a decision class. Two rounds is the
default *forever* — extra rounds mostly buy conformity, not accuracy — and every
run ends in one of three honest outcomes: **approved**, **rejected**, or
**escalated** (the panel could not decide; that question wants a human). A 2–1
split is information, not failure: **the minority report is the product**. A
unit that misses its round deadline abstains and the verdict is marked
*degraded* rather than silently thinner.

## Quick start

The free local eight-ball — every seat on one local model:

```
terva raati --provider ollama --model qwen3:8b "pizza for dinner?"
```

A real decision, with the evidence the panel should judge:

```
git diff | terva raati --evidence CHANGELOG.md --profile counsel "ready to release?"
```

In the browser, open the **raati** pane in `terva web`: the idle board is the
convene form (question, evidence, profile, class, level), the running board
streams each unit's deliberation live, and the **theater** button gives the
fullscreen console (Esc exits). Past deliberations are browsable on the idle
board with verdict filters and question search.

Agents can convene a panel themselves through the **`raati_convene`** tool —
opt-in and approval-gated, because a convening spends real sub-agent turns:

```json
// $TERVA_HOME/config.json
{ "raati": { "convene_tool": true } }
```

The tool's description tells the agent when a panel is worth it (high-stakes,
ambiguous, or gate-classed decisions — never routine ones), what it costs, and
that the minority report and any open panel questions must be read, not skimmed.

## Decision classes

| class | rule | use for |
|---|---|---|
| `advisory` (default) | majority; dissent attached | most decisions |
| `gate` | unanimity; **fails closed** on any dissent, abstention, or absence | "should this merge/ship?" checks |
| `veto` | majority, but one seat may block (default MAGATAMA-3; `--veto-holder`) | calls where human impact must be able to overrule expedience |

## The rigor ladder

Who sits on the panel is the difference between theater and an instrument:

| level | nickname | seats | honest label |
|---|---|---|---|
| 0 | *kaiku* (echo) | every seat on the invocation's provider/model | **correlated** — same weights, different priors; triage and fun, never a gate |
| 1 | *kuoro* (choir) | the host provider's weak/medium/strong tier ladder | partial decorrelation (shared lineage) |
| 2 | *käräjät* (assembly) | three exact cross-provider seats from `raati.level2` | real error decorrelation — gate-grade |

Level 1 needs a full ladder for the provider (`terva models tiers`, config
`swarm_tiers`). Level 2 needs three seats in the user config:

```json
{
  "raati": {
    "level2": [
      { "provider": "anthropic", "model": "claude-sonnet-5" },
      { "provider": "openai-codex", "model": "gpt-5.5" },
      { "provider": "ollama", "model": "qwen3:32b" }
    ]
  }
}
```

The seats line on every verdict says exactly which model held which prior —
that mapping is part of the verdict's meaning. How the level's model pool deals
onto seats is `raati.seat_order` (or `--seat-order`): `convene` (default)
shuffles once per convening so no model owns a prior across deliberations,
`fixed` keeps pool order (remappable via `raati.seat_map`) for run-to-run
comparability, and `turn` reshuffles per voting round — the strongest defense
against model↔prior fusion, at the cost of respawning seats cold.

## Inquiries — rounds must add information

With inquiries enabled, each panelist may pose up to **two questions** after
the blind round. A clerk answers them **strictly from the question and evidence
the convener supplied** — never from its own knowledge — and the pooled Q&A
rides into cross-examination. What the record cannot answer is recorded as
**open**: the panel decides with the gap named, and the verdict shows it
("not in the record — decided with this open"). Open questions are unmet
evidence: reconvening with answers beats re-rolling the same packet.

Two modes on the web form (`inquire`): **record** (the clerk alone) and
**convener** — questions the clerk can't answer become ask-dialogs on your
session, one per question under a shared five-minute budget; dismissing (or
timing out) leaves the question honestly open. The agent tool supports record
mode; `terva raati` has no clerk wired yet and says so if a profile asks.

**Convergence** (`converge`, or rounds=3 on the form): permits ONE extra
reveal round, run only when cross-examination actually flipped a verdict — it
stabilizes mutual revisions and never resolves an escalated split.

## Convening profiles

A profile is a named bundle: the caller — form, CLI flag, or agent — picks
**which** profile by name; only your config says **what** it means. Seat
composition never crosses the call boundary: a profile may pin seats, a calling
agent may not (the entity being judged must not seat its judges).

terva ships four:

| name | class | shape | level |
|---|---|---|---|
| `triage` | advisory | one blind round | 0 — the deliberate cheap eight-ball |
| `counsel` | advisory | record inquiries, converge | auto |
| `code-review` | gate | record inquiries, converge | auto |
| `ethics` | veto | convener inquiries, converge | auto |

**Auto level** resolves at convene time to the highest rigor your config
supports (complete `raati.level2` → 2, full tier ladder → 1, else 0) — a fresh
install convenes correlated today and upgrades itself the day you configure
tiers or level2, no profile edits. `"auto:1"` caps the climb (keep a profile on
one provider's ladder even when cross-provider seats exist).

**The gate honesty rule:** a gate-class profile whose auto level lands on 0
refuses with guidance — a correlated panel cannot hold a gate. An *explicit*
level-0 gate still convenes; that's your deliberate call. Veto proceeds at 0
with the usual disclosure.

Your own profiles live under `raati.profiles`; a profile with a built-in's
name replaces it wholesale, and `"builtin_profiles": false` hides the shipped
set:

```json
{
  "raati": {
    "profiles": {
      "arch-review": {
        "description": "architecture decisions: gate-grade, cross-provider, questions welcome",
        "level": "auto",
        "class": "gate",
        "inquire": "record",
        "converge": true
      },
      "quick-ethics": {
        "description": "fast human-impact read on one provider's ladder",
        "level": "auto:1",
        "class": "veto",
        "single_round": true
      }
    }
  }
}
```

Per-profile fields: `description` (the selection signal — agents choose by it),
`level` (0–2, `"auto"`, `"auto:N"`), `seats` (exact per-seat pins; implies
level 2 and replaces `raati.level2` for that profile), `seat_order`/`seat_map`,
`class`, `single_round`, `inquire` (`"record"`/`"convener"`), `converge`.
Everything is a default: whatever the call itself specifies wins, except seats.

## Records

Every deliberation persists in full — both rounds' ballots, the inquiry
docket, the tally, the minority report, and each seat's binding — under
`$TERVA_HOME/raati/raati-<timestamp>.json`. The web board's idle view browses
the archive (filter by verdict, search questions); the CLI prints the record
path with the verdict.

## Costs, honestly

A deliberation is 3 units × 2 rounds ≈ **3–4× a single query** at the chosen
binding, plus a small convener overhead. Inquiries add one clerk pass;
convergence adds up to three more unit turns *only* when a verdict flipped;
`single_round` roughly halves the total. Level 0 on local models is free.
Units cannot share prompt cache with each other (their charters diverge the
prefix at byte zero), but each unit caches normally across its own rounds.

## Config reference

| key | meaning |
|---|---|
| `raati.convene_tool` | offer the opt-in `raati_convene` agent tool (default off) |
| `raati.level2` | the three exact cross-provider seats for level 2 |
| `raati.seat_order` | `convene` (default) / `fixed` / `turn` — pool→seat policy |
| `raati.seat_map` | index permutation for `fixed` (e.g. `[2,0,1]`) |
| `raati.profiles` | named convening bundles (see above) |
| `raati.builtin_profiles` | `false` hides the shipped profile set (default on) |
| `swarm_tiers` | per-provider weak/medium/strong ladder — feeds level 1 |

All of it is **user-layer config only**: a cloned project can neither enable
deliberation spend nor redirect which models sit on the panel.

Design record: [proposals/archive/raati-deliberation.md](proposals/archive/raati-deliberation.md) —
remaining work: [proposals/raati-deliberation.md](proposals/raati-deliberation.md).
