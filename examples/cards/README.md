# Example character cards

Synthetic, **original** SillyTavern Character Card V2 cards — safe to ship (no
copyrighted content) and used as terva's card regression fixtures.

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
