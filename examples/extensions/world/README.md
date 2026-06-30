# world — an extension that is a *world*

Most extensions add a capability (a tool, a panel, a guard). This one shows a
different shape: **an extension can be a place the agent inhabits.** Its tools
are the agent's senses and effectors, its persisted state is the world's memory,
and its live context card is the agent's awareness of its surroundings. The
extension is the *referee* — it owns what is true — and the agent (in a persona)
is the *explorer* who perceives, decides, and acts.

It's deliberately generic — a small procedural wilderness of forests, ridges,
marshes, and ruins — so you can lift the shape for any simulator: a dungeon, a
building, a market, a star system.

## What it demonstrates

- **A deterministic procedural world.** Same seed ⇒ same land, so the fiction
  stays coherent across turns and sessions. Set the seed in `/extensions`.
- **Persisted, consequential state** via the SDK's `DataFS` (`world.json`):
  where you are, your stamina, the hour and day, what you've gathered, and a
  fog-of-war record of where you've been.
- **The three model-and-UI surfaces:** a live **context card** pushed every
  turn (the agent's proprioception — terrain, what's in reach, who's present,
  the ways on), a **status-line segment** (`🧭 (3,5) reed marsh · morning d1 ·
  stamina 88%`), and a fog-of-war **map panel** (`/map`).
- **`ext.Sequential()` on the effectors.** `travel`, `interact`, and `rest`
  mutate shared state with ordering constraints, so they're marked
  `Sequential()` — calls run one at a time in the order the agent issued them,
  even when the model emits several at once. Read-only tools (`observe`,
  `inventory`, `status`) stay concurrent.

## The tools

| tool | role | execution |
|---|---|---|
| `observe` | perceive the current region | read-only, concurrent |
| `travel` | move one region N/S/E/W (costs stamina + time) | `Sequential()` |
| `interact` | examine / gather / use / talk | `Sequential()` |
| `rest` | recover stamina (costs time) | `Sequential()` |
| `inventory` | list what you carry | read-only, concurrent |
| `status` | full situation report | read-only, concurrent |
| `journal` | record an entry (read with `/journal`) | local-data |

## A bundled persona

The extension ships its own persona, **Wayfarer** (`personas/wayfarer.md`), so
installing it gives you both the *capability* (the world) and an *identity* for
using it. Because it declares `good_for`, a coordinator can dispatch it to a
swarm sub-agent. (Make it `immersive: true` on a build that supports immersive
personas to have it fully own the identity.)

## Run it

From the repo root (runs from source, no build step). `--play` puts terva in
world mode — built-in coding tools off, the world's tools on, coding identity
and chrome gone:

```bash
terva --play --ext ./examples/extensions/world --persona ./examples/extensions/world/personas/wayfarer.md
```

(Without `--play` the world still works; you just also get terva's coding
identity and built-in tools alongside it.)

Then talk to your explorer:

```
Where am I? Look around, then head toward higher ground.
```
```
Travel north and gather whatever's worth taking there.
```
```
I'm tired. Make camp, then carry on east at first light.
```

Open the map any time with `/map`, and read your journal with `/journal`.

## Why `Sequential()` matters here

"Travel north and gather whatever's there" is two tool calls — `travel` then
`interact(gather)` — and the model may emit them together. The gather only makes
sense *after* the travel (the item is in the northern region, not where you
started). Without ordering, the SDK would run both on separate goroutines and
they could apply in either order. `Sequential()` guarantees the SDK preserves
the order the agent issued them. It does **not** reorder anything — issuing a
sensible order is still the agent's job (see
[docs/extensions.md](../../../docs/extensions.md#tool-call-ordering-extsequential)).

The session that wrote this verified it by firing `travel north` and
`gather` *together* (no wait between them) and confirming the gather acts on the
post-travel region every time.
