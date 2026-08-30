# Recording a terva session

Notes for capturing a terva TUI session with a terminal recorder
([asciinema](https://docs.asciinema.org/) and friends), and for making the
resulting cast small enough to publish and structured enough to analyse.

A recorder captures the byte stream terva writes, not its screen. That makes two
things worth tuning before you hit record: **how many bytes terva emits**, and
**how much of the session's structure survives in them**.

## Why a default terva makes a large recording

terva's renderer is differential — an idle frame emits nothing at all. But while
a turn is in flight the status bar is never idle: the spinner glyph advances, the
elapsed counter ticks, and token counters move. Every repaint therefore writes a
small diff, and a recorder wraps each write in its own timestamped event.

Three settings decide how often that happens.

| Setting | Default | What it drives |
|---|---|---|
| `TERVA_REDRAW_FPS` | 30 | The cap on repaints per second **while a turn is busy**. See [tui.md](tui.md#redraw-rate). |
| `spinner_interval_ms` (theme) | 80 | How often the spinner advances to its next frame. See [themes.md](themes.md). |
| animation tick | 120 ms | The floor on how often a busy turn repaints at all — about 8/s. Not configurable. |

The animation tick means a turn spent *thinking* already repaints only ~8 times a
second whatever the fps cap says. The cap matters most while text streams, which
is when repaints can reach the full 30/s.

Note also that frames containing inline images are full-repainted rather than
diffed, so `TERVA_INLINE_IMAGES=off` is worth setting for a recording that would
otherwise capture images you do not need.

## A recording profile

Nothing here is a special mode — these are the ordinary knobs, set to
recording-friendly values:

```bash
TERVA_REDRAW_FPS=4 \
TERVA_PROGRESS=on \
TERVA_INLINE_IMAGES=off \
  asciinema rec -i 2 -t "terva: <what this demo shows>" demo.cast
```

Then, inside that shell, run `terva` as usual.

`-i 2` caps idle gaps at two seconds in the recording. Set it at record time: the
player's clock is the sum of `min(gap, idle_limit)`, so the idle limit changes
what "3:40 into the video" means. Passing `-t` writes the title into the cast
header, which keeps downstream tooling from having to guess.

**Pair the spinner interval with the fps cap.** At `TERVA_REDRAW_FPS=4` terva
paints every 250 ms, while the default spinner advances every 80 ms — so the
spinner skips two frames out of three and looks jerky. A theme that sets
`spinner_interval_ms` to match the paint interval advances it exactly one frame
per paint:

```json
{
  "name": "recording",
  "description": "Slow spinner matched to a throttled redraw, for screen recordings.",
  "spinner_interval_ms": 250
}
```

Install it as a user theme and select it with `TERVA_THEME=recording`
([themes.md](themes.md)).

Measure rather than trust a number here: run a short capture both ways and
compare `wc -c`. How much you save depends on how much of your session is
streaming text versus waiting on tools.

There is a floor. The status bar renders an elapsed-seconds counter, so a busy
turn writes at least one diff per second no matter how hard you throttle.

## Finding the structure afterwards

`TERVA_PROGRESS=on` makes terva emit an **OSC 9;4** progress sequence on every
busy/idle transition — one when a turn starts, one when it ends, and nothing in
between ([tui.md](tui.md#busy-signal-terva_progress)):

| Transition | Bytes |
|---|---|
| turn started | `ESC ] 9 ; 4 ; 3 ; 0 ESC \` |
| turn ended | `ESC ] 9 ; 4 ; 0 ; 0 ESC \` |

Grep a cast for those and you have every "the agent was working here" interval in
the session, with timestamps.

This is worth preferring over the obvious alternative. Detecting work by looking
for the spinner means matching on `spinner_frames`, `spinner_messages`, or
`spinner_interval_ms` — all of which are theme data that any user can override,
so spinner-based detection breaks the first time someone records with a custom
theme. The progress sequence is fixed and carries no theme data.

`TERVA_PROGRESS=on` forces emission even on a terminal that would not otherwise
be sent the sequence, which is what you want while recording: the bytes land in
the capture whether or not the terminal you happen to be using does anything
visible with them.

## Secrets

**terva does not redact anything on its way to the screen.** A token echoed by a
`bash` tool call, an API key pasted into the login dialog, a secret in a file the
agent `read`s — all of it is drawn, and a recorder captures it verbatim. The
capture is also far harder to fix than the screen was: the value can appear in
hundreds of separate frames as the transcript scrolls.

So: prefer a throwaway credential for anything you record, and scan the finished
cast for the values you care about before publishing it. Treat a recording that
ever displayed a live secret as a leak of that secret and rotate it, rather than
trying to scrub the file.

## Limits

Worth knowing before you plan tooling around this:

- **terva emits no chapter markers.** The asciicast format has a marker event
  (`"m"`), but there is no in-band escape sequence a recorded program can write
  to produce one — markers are added by the recorder or by editing the file
  afterwards. Deriving them from the busy/idle transitions above, or from a
  session transcript under `$TERVA_HOME/sessions/`, is the practical route.
- **The idle limit is not in the file.** `-i` is applied by the recorder as it
  writes; nothing in the cast header records what it was, unless you put it
  there. Note the value alongside the recording.
