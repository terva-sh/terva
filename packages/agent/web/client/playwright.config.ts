import { defineConfig, devices } from '@playwright/test'

// Opt-in browser smoke harness for the web control panel. This is deliberately
// NOT part of `npm test` (the vitest/happy-dom component contracts) or the
// hermetic `just ci` gate: it needs a real Chromium (`npx playwright install
// chromium`), which the standard-tools architecture keeps in recommended
// extension/MCP territory rather than the core toolchain. Run it explicitly
// (`npm run test:smoke` or `just web-smoke`) to cover the durable, hard-to-
// unit-test browser behaviors — see tests/smoke/README.md for scope.

const PORT = Number(process.env.SMOKE_PORT ?? 4173)

// CI runs this on the same musl (Alpine) image as every other job, where
// Playwright's OWN Chromium — a glibc build — cannot start at all. The job
// installs the distro's chromium and names it here.
//
// Unset locally, so a developer still gets Playwright's pinned browser, which is
// the one to trust when the question is layout. CI's job is narrower: catch the
// suite going stale against a UI that moved under it, and a slightly different
// Chromium does that just as well. --no-sandbox because the container runs as
// root; --disable-dev-shm-usage because the default /dev/shm in a container is
// 64MB and Chromium will crash rendering into it.
const systemChromium = process.env.PLAYWRIGHT_CHROMIUM_PATH
  ? {
      launchOptions: {
        executablePath: process.env.PLAYWRIGHT_CHROMIUM_PATH,
        args: ['--no-sandbox', '--disable-dev-shm-usage'],
      },
    }
  : {}

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
  // ⚠️ Left at the defaults ON PURPOSE. When the gate first flaked on the
  // runner, one worker and a 15s assertion budget were the obvious reach — and
  // both were wrong: the failure was a lost click, permanent, still red after
  // 15s and after a retry. Widening the budget would have hidden nothing and
  // slowed every real failure down. The cause was a test racing a header that
  // was still assembling (stage-you-studio), and it is fixed there.
  //
  // If this suite flakes again, get the artifact (the CI job uploads the trace
  // and a screenshot on failure) and find the cause. Do not start here.
  expect: { timeout: 5_000 },
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: 'on-first-retry',
    // Uploaded as an artifact when CI goes red (see .forgejo/workflows/ci.yml):
    // a browser failure that only happens on the runner leaves nothing else
    // behind to read.
    screenshot: 'only-on-failure',
    // The panel is a PWA; block its service worker so it can't cache assets or
    // interpose on navigation between runs, and so page.routeWebSocket is the
    // only thing standing in for the backend.
    serviceWorkers: 'block',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'], ...systemChromium } },
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
    // Local reuse is a speed choice for repeat runs, and it has a sharp edge: a
    // server left over from an earlier build serves THAT bundle, so the suite
    // silently reports on code you are no longer running. `just ci-web-smoke`
    // refuses to start when this port is occupied for exactly that reason —
    // keep the two in step if this line or PORT changes.
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
})
