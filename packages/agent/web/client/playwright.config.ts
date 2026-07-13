import { defineConfig, devices } from '@playwright/test'

// Opt-in browser smoke harness for the web control panel. This is deliberately
// NOT part of `npm test` (the vitest/happy-dom component contracts) or the
// hermetic `just ci` gate: it needs a real Chromium (`npx playwright install
// chromium`), which the standard-tools architecture keeps in recommended
// extension/MCP territory rather than the core toolchain. Run it explicitly
// (`npm run test:smoke` or `just web-smoke`) to cover the durable, hard-to-
// unit-test browser behaviors — see tests/smoke/README.md for scope.

const PORT = Number(process.env.SMOKE_PORT ?? 4173)

export default defineConfig({
  testDir: './tests/smoke',
  // *.smoke.ts, not *.spec.ts, so vitest's default glob (**/*.{test,spec}.ts)
  // never collects these — they import @playwright/test and run under this
  // runner only. See tests/smoke/README.md.
  testMatch: '**/*.smoke.ts',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? 'line' : 'list',
  timeout: 30_000,
  expect: { timeout: 5_000 },
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: 'on-first-retry',
    // The panel is a PWA; block its service worker so it can't cache assets or
    // interpose on navigation between runs, and so page.routeWebSocket is the
    // only thing standing in for the backend.
    serviceWorkers: 'block',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
    // Mobile-Safari-like engine, scoped to the pane-overflow guard: the
    // settings pane's horizontal-scroll bug was WebKit-only (a select's
    // untruncated option text kept counting as scrollable overflow), so
    // Chromium alone cannot defend it. Needs `npx playwright install webkit`.
    { name: 'webkit-mobile', use: { ...devices['iPhone 13'] }, testMatch: '**/overflow.smoke.ts' },
  ],
  webServer: {
    // Serve the actual built artifact (the dist/ that go:embed ships into the
    // binary), not the dev server — the smoke suite should exercise what users
    // actually get. The backend WebSocket is mocked per-test (page.routeWebSocket),
    // so no terva server, workspace, or credential is needed.
    // --host 127.0.0.1 pins the IPv4 loopback; vite preview otherwise binds
    // "localhost" (IPv6 ::1 on some machines), which the 127.0.0.1 url never sees.
    command: `npm run build && npm run preview -- --host 127.0.0.1 --port ${PORT} --strictPort`,
    url: `http://127.0.0.1:${PORT}`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
})
