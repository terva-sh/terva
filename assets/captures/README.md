# Documentation captures

Real captures and hand-drawn diagrams that README and the terva.sh landing
page embed, and that pages under `docs/` link to. Decision 0020 in the
development repository says why they live here and not under `docs/`.

## Rules

- A capture is taken from the running product. A diagram is drawn by hand as
  an SVG. Nothing here is a mock of an interface.
- Each file has a row below naming its recipe and its stamp. The stamp is a
  sidecar `<name>.stamp` holding the commit the capture was taken at, and the
  terminal size or viewport it was taken in.
- Desktop captures are about 1700 pixels wide, mobile captures about 700, as
  WebP under 100 KB. Bytes committed here stay in history.
- The landing page keeps its own copy under `docs/vanity/site/assets/`. The
  site gate fails when that copy differs from the file here.

## Files

| File | Shows | Recipe | Stamp |
|---|---|---|---|
| `terminal-demo.jsonl` | The source transcript of the terminal demo: a fix in `auth.go`, two permission prompts, one question. Hand-authored, no real provider, no secret. | `UPDATE_FIXTURES=1 go test ./packages/agent/replay/ -run TestDemoTranscript` rewrites it from the scene in `demo_transcript_test.go`; the same test without the flag checks it still replays. | none; it is source |
| `terminal-demo.cast` | The terminal demo, recorded by replaying the transcript through the real TUI. | `just demo-record` | `terminal-demo.cast.stamp` |
| `terminal-demo.gif` | The same cast rendered for README, which cannot run a player. | `just demo-gif` | shares the cast's stamp |

## Recipes

A recipe is what one person follows to remake the capture in ten minutes.
Write it here when the capture lands, with the command that starts the
product, the state to reach, and the tool that takes the picture. The web
panel recipe in `docs/vanity/LAYOUT.md` is the model.

### The terminal demo

The demo is a replay, so the recording needs no provider and no credential,
and it comes out of whatever the TUI looks like at the commit you run it at.

1. Install `asciinema` (the recorder) and `agg` (the cast-to-GIF renderer).
2. Run `just demo-record`. It builds terva, replays
   `assets/captures/terminal-demo.jsonl` inside `asciinema rec` at 100 by 30
   with the recording profile from `docs/recording.md`, ends the replay after
   the scene, writes `terminal-demo.cast`, and stamps it with the commit and
   the terminal size.
3. Run `just demo-gif` to render `terminal-demo.gif` from the cast.
4. Copy the cast to `docs/vanity/site/assets/` for the landing page. The site
   gate holds the copy byte-identical to the file here. The player that plays
   it there is vendored beside it (asciinema-player v3.17.0, two files from
   its GitHub release); upgrade it by replacing those two files.

The recipe replays at half speed (`--speed 0.5`), and the page and README say
so. At that pace the scene runs under a minute: the text and the pauses the
person took, which the replay holds up to three seconds each before it walks
the cursor to their answer.
