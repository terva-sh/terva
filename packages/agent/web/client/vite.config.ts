import { defineConfig } from 'vite'
import preact from '@preact/preset-vite'
import { VitePWA } from 'vite-plugin-pwa'

// The build output (dist/) is embedded into the terva binary via go:embed
// (see ../assets.go), so it must be fully self-contained. base: './' keeps
// asset URLs relative in case the panel is ever mounted under a proxy subpath.
export default defineConfig({
  base: './',
  plugins: [
    preact(),
    VitePWA({
      registerType: 'autoUpdate',
      includeAssets: ['favicon.svg', 'favicon.ico', 'apple-touch-icon.png'],
      manifest: {
        name: 'terva',
        short_name: 'terva',
        description: 'Your terva agent, anywhere.',
        // Deep Tar — the brand's app-icon background, so the PWA splash blends
        // seamlessly into the icon and the app chrome (see assets/brand).
        theme_color: '#0A0A09',
        background_color: '#0A0A09',
        display: 'standalone',
        start_url: '.',
        scope: '.',
        // any = the polished rounded app icon; maskable = the full-bleed square
        // (logo in the safe zone, dark to every edge) so platform masks apply
        // cleanly without clipping a pre-rounded corner.
        icons: [
          { src: 'pwa-192.png', sizes: '192x192', type: 'image/png' },
          { src: 'pwa-512.png', sizes: '512x512', type: 'image/png' },
          { src: 'pwa-maskable-512.png', sizes: '512x512', type: 'image/png', purpose: 'maskable' },
        ],
      },
      workbox: {
        // NO `html`. This is the second half of the fix below, and it is the
        // half that was missed — the shell must not be PRECACHED either.
        //
        // Dropping navigateFallback (below) removes the NavigationRoute, but
        // workbox's PrecacheRoute answers a navigation to "/" all by itself:
        // its match generates URL variations and, with the default
        // `directoryIndex: 'index.html'`, "/" becomes "/index.html" — which is
        // in the precache manifest the moment `html` is in these globs. That
        // route is registered by precacheAndRoute BEFORE our runtime route, so
        // it wins, and it is cache-FIRST. Every navigation to the panel's own
        // start_url was still answered from disk; the NetworkFirst route below
        // was dead code for the only URL the PWA ever navigates to.
        //
        // The daemon's 401 login form was therefore STILL unreachable, and a
        // client that lost its credential was still stranded forever, quietly
        // retrying its WebSocket — the exact bug the comment below describes,
        // through a second door.
        //
        // The shell needs no precache entry: the runtime route caches it on
        // every successful navigation, so offline still works. Precaching it
        // only ever bought a first-load-while-offline that cannot happen.
        globPatterns: ['**/*.{js,css,svg,png,woff2}'],
        // Explicitly undefined, not merely absent: vite-plugin-pwa's own default
        // for this is 'index.html' (defaultWorkbox, dist/index.js), applied with
        // Object.assign — which copies an explicit undefined but not a missing
        // key. Omitting the line therefore does NOT omit the route; it silently
        // reinstates the cache-first NavigationRoute described below, registered
        // ahead of ours so ours never matches. Deleting this line is a regression.
        navigateFallback: undefined,
        // Navigations go to the NETWORK first, and fall back to the cache only
        // when the network does not answer at all.
        //
        // The obvious config for an SPA is `navigateFallback: 'index.html'`, and
        // it was what this used. But that registers a NavigationRoute bound to the
        // PRECACHED index.html, which is cache-FIRST: every navigation is answered
        // from disk and the server is never consulted. That is fine until the
        // server has something to say — and it does. An unauthenticated request is
        // answered with a 401 carrying the login form, and a cache-first worker
        // threw that form away and served the app shell over the top of it. The
        // panel then booted, opened its WebSocket, was refused, and retried
        // forever: signed out, unable to say so, and unable to offer the one thing
        // that would fix it. Reloading could not help — the reload was served from
        // the same cache. The app could not even be repaired by shipping a fix,
        // because the worker holding it hostage was the thing that had to change.
        //
        // NetworkFirst returns whatever the server says, 401 included, and only
        // reaches for the cache when the fetch fails outright. Offline still works;
        // the server just gets to decide who is allowed in. This panel is useless
        // without a live socket to the daemon anyway, so a cached shell was never
        // worth much — and it was never worth this.
        runtimeCaching: [
          {
            urlPattern: ({ request }) => request.mode === 'navigate',
            handler: 'NetworkFirst',
            options: {
              cacheName: 'terva-shell',
              networkTimeoutSeconds: 5,
              // Cache only a real page. A 401 is an answer, not an app, and
              // storing one would reintroduce exactly the bug above.
              cacheableResponse: { statuses: [200] },
            },
          },
        ],
      },
    }),
  ],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // Multi-page app: the panel (index.html) and the Stage app (stage.html) are
    // two entries in ONE client workspace. Rollup chunk-splits the shared
    // platform/ui/features code, so the apps share bytes in dist rather than
    // duplicating them. One vitest, one typecheck, one i18n catalog, one embed.
    rollupOptions: {
      input: {
        main: 'index.html',
        stage: 'stage.html',
      },
    },
  },
})
