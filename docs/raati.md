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
It also coaches the agent to convene **by profile and omit `level`** — the
agent can't see your config, so an explicit level can only cap the panel below
what auto would seat, or demand a rung that doesn't exist. When a panel does
seat correlated (level 0, or a one-model thinking ladder), the verdict itself
says so, so the caveat survives in the transcript; and the description orders
a refused convening to be surfaced as "no panel ran" rather than silently
absorbed into "reviewed".

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
| 1 | *kuoro* (choir) | the host provider's weak/medium/strong tier ladder | partial decorrelation (shared lineage — or, on a thinking ladder, shared weights) |
| 2 | *käräjät* (assembly) | three exact cross-provider seats from `raati.level2` | real error decorrelation — gate-grade |

### A level-1 ladder can be one model at three thinking levels

`swarm_tiers` rungs can name a *reasoning effort* as well as a model ([models.md](models.md#a-rung-can-name-a-thinking-level-instead-of-a-model)), and level 1 seats whatever the ladder resolves to. So a provider with one good model still has a panel:

```json
{ "swarm_tiers": { "kimi": {
  "weak":   { "model": "k3", "reasoning": "off" },
  "medium": { "model": "k3", "reasoning": "medium" },
  "strong": { "model": "k3", "reasoning": "high" } } } }
```

A reasoning model with thinking off really is a different judge from the same weights at high — different failure modes, different confidence, different appetite for saying "I don't know". That is a genuine advisory panel, and the verdict's seats line names each seat's effort (`kimi/k3 @off`) so a reader can weigh it.

It is **not three independent judges**, and two things follow:

- **It cannot hold an auto-resolved gate.** The honesty rule reads the resolved seats, not the level number: a gate whose seats all carry the same weights refuses, and says which of the two problems it is — identical seats, or one model spanning efforts. An *explicit* level is still your call, as it always has been.
- **Its default seat order becomes `turn`.** With three different models, shuffling once per convening is enough — the weights disagree on their own. With one model at three efforts the *only* thing distinguishing the seats is the effort, so holding it fixed for a whole deliberation fuses "the benevolence seat" with "the one that wasn't thinking". Rotating per round keeps the effort a property of the round instead of the prior. Only the default moves: `turn` respawns every seat cold in round two (no cross-round prompt cache, evidence re-read per seat), and writing `"seat_order": "convene"` refuses that trade.

Level 1 needs a **full** weak/medium/strong ladder for the host provider.
terva ships one for Anthropic, GitHub Copilot, Google, OpenAI and Codex, so on
those a fresh install convenes at level 1 with no configuration — which is also
what lets a gate-class profile like `code-review` run at all (see the gate
honesty rule below). DeepSeek and Kimi ship two rungs, which is enough for a
cheap swarm spawn but **not** a ladder; on those, and on every gateway, either
configure `swarm_tiers` or go to level 2. Check with `terva models tiers`.

Level 2 needs three seats in the user config:

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

### Letting terva seat level 2 for you

Writing three bindings by hand is the only way to reach level 2, and a model
that reaches for a gate profile before you have done so gets a refusal that
reads like being denied permission. `raati.auto_panel` closes that: with it on
and no `raati.level2`, terva seats the panel from the **strong tier rung of
each provider you are logged into**.

```json
{
  "raati": {
    "auto_panel": true,
    "auto_panel_providers": ["anthropic", "openai-codex", "kimi"]
  }
}
```

`terva models tiers` prints exactly what it would seat, and what it passed over
and why — run it before you rely on it.

Three things it will not do:

- **It will not turn itself on.** Every other raati default is a *shape* (how
  many rounds, which vote rule). This one **spends**, at each provider's most
  expensive model, six sub-agent turns at a time, on credentials you may have
  added for something else entirely. Off until you say so; while off, level 2
  is exactly as unavailable as it is today.
- **It will not seat the same weights twice.** Anthropic and GitHub Copilot are
  two logins, two bills, and one set of Claude. A panel of three Claudes is
  level 0 wearing level 2's label — and the label is what a gate is trusted on.
  One seat per model lineage.
- **It will not under-fill.** Two providers is not a three-seat panel, and
  padding the third seat from a provider already seated rebuilds the exact
  correlation the level rules out. It seats every seat or none, and says which
  provider was missing.

`auto_panel_providers` restricts and orders the draw. Without it the order is
the provider registry's — the order `terva models tiers` lists in, which is
arbitrary with respect to quality. terva does not rank your providers; if you
have five and want a particular three, name them.

An explicit `raati.level2` always wins. Writing the seats down is you deciding,
and a derivation must never overrule that.

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

**The gate honesty rule:** a gate-class profile whose auto level resolves to
seats that all carry the same weights refuses with guidance — a correlated
panel cannot hold a gate. It reads the resolved SEATS, not the level number,
because a level-1 ladder can now be one model at three thinking efforts (see
above); the two cases get different messages because they have different
fixes. An *explicit* level still convenes; that's your deliberate call. Veto
proceeds with the usual disclosure.

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
| `raati.auto_panel` | seat level 2 from your logged-in providers' strong tiers (default **off** — it spends) |
| `raati.auto_panel_providers` | restrict and order the auto panel's draw |
| `raati.seat_order` | `convene` (default) / `fixed` / `turn` — pool→seat policy |
| `raati.seat_map` | index permutation for `fixed` (e.g. `[2,0,1]`) |
| `raati.profiles` | named convening bundles (see above) |
| `raati.builtin_profiles` | `false` hides the shipped profile set (default on) |
| `swarm_tiers` | per-provider weak/medium/strong ladder — feeds level 1 |

All of it is **user-layer config only**: a cloned project can neither enable
deliberation spend nor redirect which models sit on the panel.

Design record: `docs/proposals/archive/raati-deliberation.md`, with the remaining
work in `docs/proposals/raati-deliberation.md` — both in the development
repository, not the public release tree.
