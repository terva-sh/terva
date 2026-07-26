# Web control-panel browser smoke tests

A small, **opt-in** Playwright suite that exercises the durable browser behaviors
the happy-dom component tests (`src/**/*.test.ts`) can't: real layout, scrolling,
focus, and clipboard/drag plumbing.

It is **not** part of `npm test`, `just web-check`, or the hermetic `just ci`
gate — those stay Node-free and browser-free. It IS gated in the pipeline, as the
`Web Client Smoke` job (chromium only), which is what stops it rotting; see the
audit below for why that became necessary. Run it locally too, with
`just web-smoke`, when you touch the web client's interaction layer.

## Running

```sh
# one-time (or when the pinned Playwright version changes):
npm ci
npx playwright install chromium webkit # Linux: add --with-deps

# run the suite:
npm run test:smoke                      # from packages/agent/web/client
# or, from the repo root:
just web-smoke
```

The Playwright config (`../../playwright.config.ts`) builds the client and serves
the real `dist/` with `vite preview`; there is **no terva backend**. Each test
mocks the control-plane WebSocket (`/ws`) with `page.routeWebSocket`, playing just
enough of the ctrlproto handshake (`installMockBackend` in `support.ts`) for the
app to connect, select a session, and receive transcript events.

## Scope — durable critical flows only

Each case defends a behavior that genuinely needs a real browser: layout,
scrolling, focus, selection, clipboard/drag plumbing. Anything a component
contract can assert belongs in a vitest `*.test.ts` instead — those run in CI,
and these do not.

**This suite used to rot silently, because nothing ran it.** A 2026-07 audit
found 18 of its tests red on a clean `sothr-main`, none of them reporting a real
defect:

| what had changed under them | tests |
|---|---|
| the panel stopped adopting the server's global `current` session, so a fresh tab lands on the picker (`platform/bootsession`) | 14 |
| the character studio began mounting both tabs at once, so `.stage-editfield`/"Name" matches the persona pane too | 1 |
| a favourited model renders in BOTH `★ Favorites` and its provider group | 1 |
| ✏️ on a World-tab character stopped running the doctor on click (it fired before the model picker was visible) | 1 |

Every one was a deliberate product change whose smoke coverage was not updated,
and the suite had no way to say so. It is gated now, so the PR that moves the UI
is the one that hears about it.

⚠️ The `webkit-mobile` project is still ungated (it would double the browser
install for three overflow cases), so THAT half can still drift. Run the full
`just web-smoke` when you touch pane layout, and treat a red baseline as a
finding rather than as something your working tree did.

## Conventions

- Files are named `*.smoke.ts` (not `*.spec.ts`) so vitest's default glob never
  collects them — they use the Playwright runner, not vitest.
- `support.ts` holds the shared WebSocket mock and helpers; it is not a test file.
- **A test that needs the panel's session shell must ask for one**: navigate to
  `panelSessionURL`, not `/`. A fresh tab deliberately adopts no session and
  renders the picker, and `SMOKE_SESSION` has to keep the daemon's
  `YYYYMMDD-HHMMSS-<8 hex>` id shape or `?session=` is silently discarded.
- Use `editButtonFor(page, text)` rather than clicking a bubble to edit a
  message — a click on a message selects text, it does not open the editor.
- Everything runs under the `chromium` project; the `webkit-mobile` project is
  scoped (via its `testMatch`) to the tests that defend engine-specific layout
  behavior, so the rest of the suite stays single-engine.
- Keep this suite small. New cases should defend a behavior that genuinely needs
  a real browser; anything a component contract can assert belongs in a vitest
  `*.test.ts` instead.
