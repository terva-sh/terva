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
        globPatterns: ['**/*.{js,css,html,svg,png,woff2}'],
        navigateFallback: 'index.html',
        // Never let the service worker intercept the live control-plane socket
        // or the health probe.
        navigateFallbackDenylist: [/^\/ws/, /^\/healthz/],
      },
    }),
  ],
  build: { outDir: 'dist', emptyOutDir: true },
})
