# Web control-panel browser smoke tests

A small, **opt-in** Playwright suite that exercises the durable browser behaviors
the happy-dom component tests (`src/**/*.test.ts`) can't: real layout, scrolling,
focus, and clipboard/drag plumbing.

It is deliberately **not** part of `npm test`, `just web-check`, or the hermetic
`just ci` gate. Like DevTools/Playwright generally, it lives in "recommended
tooling" territory, not the core toolchain — it downloads and runs a real
Chromium. Run it explicitly when you touch the web client's interaction layer.

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

Six cases, chosen to stay useful without becoming a broad, brittle end-to-end
suite (per the W3 retrospective item):

| file | flow |
|------|------|
| `render.smoke.ts` | the shell renders and connects at desktop **and** narrow widths |
| `timeline.smoke.ts` | a pinned timeline follows streaming output; once unpinned it does **not** jump to the bottom |
| `composer.smoke.ts` | slash autocomplete keeps textarea focus, navigates by keyboard, and dismisses on Escape |
| `attachments.smoke.ts` | an image reaching the composer by drop **or** paste becomes an attachment chip |
| `overlays.smoke.ts` | a modal (model picker) closes via both Escape and a backdrop click |
| `overflow.smoke.ts` | no pane surface scrolls horizontally — every core surface at phone/tablet/rail widths, enforcing the `styles.css` layout conventions (also runs under WebKit — the settings regression was WebKit-only) |

## Conventions

- Files are named `*.smoke.ts` (not `*.spec.ts`) so vitest's default glob never
  collects them — they use the Playwright runner, not vitest.
- `support.ts` holds the shared WebSocket mock and helpers; it is not a test file.
- Everything runs under the `chromium` project; the `webkit-mobile` project is
  scoped (via its `testMatch`) to the tests that defend engine-specific layout
  behavior, so the rest of the suite stays single-engine.
- Keep this suite small. New cases should defend a behavior that genuinely needs
  a real browser; anything a component contract can assert belongs in a vitest
  `*.test.ts` instead.
