//go:build terva_web

package web

import (
	"strings"
	"testing"
)

// The service worker decides whether the daemon is ever allowed to ANSWER a
// navigation, and getting that wrong strands a client in a way no server-side fix
// can reach: the code that would repair it is the code the stranded worker refuses
// to fetch. It has now gone wrong twice, through two different doors, and neither
// was visible in any test — the config looked right both times.
//
// So gate the built artefact, not the config that produced it. These read the sw.js
// that actually SHIPS (the embedded one), which is the only thing a browser sees.
func swSource(t *testing.T) string {
	t.Helper()
	b, err := clientFS.ReadFile("client/dist/sw.js")
	if err != nil {
		t.Fatalf("read embedded sw.js: %v", err)
	}
	return string(b)
}

// TestServiceWorkerRegistersNoNavigationRoute is door #1.
//
// vite-plugin-pwa defaults `navigateFallback` to 'index.html' and merges its
// defaults with Object.assign — which copies an explicit `undefined` but not a
// missing key. So DELETING the `navigateFallback: undefined` line from
// vite.config.ts does not remove the setting, it restores the default: a
// cache-first NavigationRoute, registered ahead of ours, answering every
// navigation from disk. It looks like dead config. It is not.
func TestServiceWorkerRegistersNoNavigationRoute(t *testing.T) {
	if strings.Contains(swSource(t), "NavigationRoute") {
		t.Error("sw.js registers a NavigationRoute: navigations are answered CACHE-FIRST, " +
			"so the daemon's 401 login form can never be seen and a client that loses its " +
			"credential is stranded. Check `navigateFallback: undefined` in vite.config.ts.")
	}
}

// TestServiceWorkerDoesNotPrecacheTheShell is door #2, and the one that was missed.
//
// Killing the NavigationRoute is not enough, because workbox's PrecacheRoute
// answers a navigation to "/" on its own: its match generates URL variations and,
// with the default `directoryIndex: 'index.html'`, "/" becomes "/index.html" — a
// precache hit the moment `html` is in the workbox globs. precacheAndRoute
// registers that route BEFORE the runtime one, so it wins, and it is cache-first.
//
// The panel's start_url is "/". So the one URL the installed app ever navigates to
// was still served from disk, the NetworkFirst route was dead code, and the fix for
// door #1 changed nothing for the client it was written for.
//
// The shell needs no precache entry: the runtime NetworkFirst route caches it on
// every successful navigation, so offline still works.
func TestServiceWorkerDoesNotPrecacheTheShell(t *testing.T) {
	sw := swSource(t)
	for _, entry := range []string{`url:"index.html"`, `url: "index.html"`, `"index.html"`} {
		if strings.Contains(sw, entry) {
			t.Fatalf("sw.js precaches the shell (%s): workbox's PrecacheRoute resolves a "+
				"navigation to \"/\" against it via the default directoryIndex, cache-FIRST and "+
				"ahead of the NetworkFirst route — so the daemon never gets to answer, and the "+
				"login form stays unreachable. Drop `html` from workbox.globPatterns.", entry)
		}
	}
}

// TestServiceWorkerStillPrecachesTheBundle is the other side of the same coin: the
// fix above must not throw offline support away with the bug. The fingerprinted
// assets are exactly what precaching is FOR — they are immutable, they carry no
// authorization decision, and the server has nothing to say about them.
func TestServiceWorkerStillPrecachesTheBundle(t *testing.T) {
	sw := swSource(t)
	if !strings.Contains(sw, `url:"assets/`) {
		t.Error("sw.js precaches no fingerprinted assets: the panel would refetch its whole " +
			"bundle on every launch and would not work offline at all")
	}
}
