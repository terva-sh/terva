# Talkoot Avatar Studio

A development page that designs, tunes, and checks the Talkoot member faces
on the product's own renderer (`TKT-01M3NSM90G`). The design is recorded in
the Talkoot members proposal, in its section "The face".

The dev server serves the studio, and the build leaves it out. `vite.config.ts`
names only `index.html` and `stage.html` as build inputs, so `dist/` and the
binary never carry it. `studio.test.ts` checks that.

## Open it

```sh
cd packages/agent/web/client
npm run dev
```

Then open `http://localhost:5173/studio.html`.

## What it draws with

Every mark on the page is the product's `FaceMark`, from
`src/features/talkoot/MemberMark.tsx`. The studio provides a `TuningContext`
with its live copy of the pose table, so every mark follows an edit as it
happens. The product draws with the same context and the committed table.

The pose table is `src/features/talkoot/face/poses.json`. It holds every
number the face uses: the eye's proportions, the poses and their strong forms,
the beats, the move time, the shut-to-turn limit, and the look (outline and
edge widths, and the reach of the drift and the reading scan).

## Save

Edits persist in the browser until Save writes them or Reset drops them.
**Save to poses.json** posts the table to `/__studio/save`, which only the dev
server has (`scripts/studio-save.ts`). The route writes `poses.json` and no
other file. It accepts JSON from the studio's own origin, and only a table the
renderer can read. Commit the result as an ordinary diff.

A new pose that Save writes also appears in the product's table. The face
tests list the poses the proposal names, so they fail until the proposal and
the tests name the new pose too.

## The transition check

**Run the transition check** samples every move between two forms on every
member body, and each form at the far end of a glance. It reports four faults:

- **spin**: a visible slit turns more than 30 degrees.
- **vanish**: both eyes nearly disappear mid-move without a shut.
- **clip**: part of an eye leaves the body. The check samples the outline of
  each slit and pupil bar, so an eye that overhangs by part of its width is a
  clip. The eye edge, which carries a patch of face over the line, does not
  count.
- **touch**: the two eyes nearly meet.

`tests/smoke/talkoot-studio.smoke.ts` runs the check in CI. It fails on any
spin, vanish, or touch. It also fails on a clip that is not in
`agreed-clips.json`, and on an agreed clip that no longer happens. To agree a
new clip, add its line to that file in the same change, and say why in the
review. The check names each clip in the form the file uses.

## URL options

| Option | Effect |
|---|---|
| `only=ID` | Show one section: `playground`, `animations`, `transitions`, `clips`, `poses`, `sidebar`, or `team`. |
| `theme=light` | The light theme. |
| `motion=subtle` or `motion=off` | Less motion. Reduced motion in the system still wins. |
| `speed=N` | Play every move, beat, and loop at N times speed. |
| `pose=NAME` | The playground's pose, such as `worried, strong`. The prototype's `brows worried` still works. |
| `body=NAME` | The playground's body. |
| `team=NAME` | The team section's body. |
| `grid=N`, `side=N` | The size of the grid and the sidebar marks. |
| `edge=MODE`, `edgeW=N` | The eye edge (`body`, `halo`, `outside`, `none`) and its width. |
| `clipBody=NAME`, `clipMax=N`, `clipSize=N` | Narrow and enlarge the clip gallery. |
| `selftest` | Play every pose, beat, presence, and the scenario, and write `ok` or the errors to `<body data-selftest>`. |
| `#ID` | Scroll to a section. |
