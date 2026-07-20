# Example character cards

Synthetic, **original** SillyTavern Character Card cards (V2 and V3) — safe to
ship (no copyrighted content) and used as terva's card regression fixtures.

> ### ⚠️ These are authored from the spec, not exported by a real editor
>
> Every card here was written by hand against the published specification. That
> makes them precise about what the spec *describes* and silent about what the
> ecosystem actually *emits* — and those two have already diverged in a way that
> cost real debugging time. A live card off chub.ai turned out to carry **two
> `chara` chunks holding different revisions of the character** (one system
> prompt twice the length of the other), which no spec-derived fixture would ever
> have produced, and which quietly changed which character you got. The same card
> also shipped a 7680×2160 / 19.6 MB portrait that broke the upload path outright.
>
> **So: replace or supplement these with cards exported by real tools** —
> SillyTavern, RisuAI, chub.ai — as they come to hand, and keep whatever
> surprises they bring as fixtures in their own right. `aava-v3` in particular
> has never been checked against a card a V3 editor actually wrote; it asserts
> that terva reads the format as specified, not that it reads what V3 tools
> produce. Treat a passing V3 fixture as necessary, not sufficient.

## `aava-v2.json` / `aava-v2.png`

*Aava*, a lighthouse keeper — an original character that exercises the full CCv2
surface: `{{char}}`/`{{user}}` macros, `<START>` example dialogue, a
`system_prompt` (with `{{original}}`), `post_history_instructions`, two
`alternate_greetings`, and a `character_book` with **keyed**, **selective**
(secondary keys), and **constant** entries.

The `.png` is a valid 64×64 image with the *same* card embedded in a `chara`
tEXt chunk (the community sharing convention). The `.json` and `.png` parse to an
identical card — `packages/agent/card` asserts this parity as a regression, and
regenerates the PNG from the JSON with:

```
UPDATE_FIXTURES=1 go test ./packages/agent/card/
```

Try it (cards are chat/play only — not valid in regular coding mode):

```
terva card info examples/cards/aava-v2.png                 # inspect, no model call
terva --card examples/cards/aava-v2.json                   # chat as Aava (implies --chat)
terva --card examples/cards/aava-v2.png --dump-prompt \
      -p "tell me about the fog-bell"                       # see the assembled prompt, offline
```

On the first interactive card session terva asks what the character should call
you (its `{{user}}` macro) and remembers the answer in your **global** config,
so it persists across projects (even under project-scoping). Override per-run
with `--as NAME`; a trusted project may set its own `user_name`. Non-interactive
runs fall back to `--as`, then the trusted-project / global saved name, then the
literal `"User"`.

The `depth_prompt` and `terva.sh/harness` blocks under `extensions` are retained
verbatim but **never interpreted as capabilities** — a card is data, never code —
and `creator_notes` is never sent to the model.

Beyond the CLI, a card can also be imported into the **character library** — the
`cards.import` control-plane verb, or a drag onto the panel's **Characters** pane
(and the immersive [Stage](../../docs/web.md#stage-the-immersive-chatplay-surface)
app). The library keeps the original file, so the avatar pixels survive and serve
over the auth-gated `/media/` route. See [docs/controllers.md](../../docs/controllers.md)
for the `cards.*` verbs and `docs/proposals/stage-surface.md` §3 for the library.

## `aava-v3.json` / `aava-v3.png`

The same character as a **Character Card V3**, exercising the V3-only surface:
`nickname`, `source`, `group_only_greetings`, `creator_notes_multilingual`,
`creation_date` / `modification_date`, an `assets` set beyond the default icon,
and a `character_book` carrying all four activation shapes terva's reader has to
cope with — a plain keyed entry, one with **regex keys** (`use_regex`), one whose
content opens with **`@@decorators`**, and a **constant** entry.

The `.png` ships the layout V3 writers actually use: a **`ccv3`** tEXt chunk
holding the V3 document, plus a **`chara`** chunk holding a genuine V2 downgrade
so V2-only tools still open the file. terva prefers `ccv3` on read, per the spec.
As with the V2 fixture, the `.json` is the source of truth and the `.png` is
regenerated from it:

```
UPDATE_FIXTURES=1 go test ./packages/agent/card/
```

terva **carries** everything above but does not yet **act** on all of it — regex
lorebook keys, `@@decorators`, and non-default assets are stored and round-tripped
but not honored, and the import reports exactly that through the card-import
warnings so a lorebook that never fires has a stated reason. Importing this card
is the fastest way to see those warnings:

```
terva card info examples/cards/aava-v3.png     # inspect, no model call
```
