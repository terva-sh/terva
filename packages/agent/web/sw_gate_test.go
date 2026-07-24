//go:build terva_web

package web

import (
	"net/http"
	"regexp"
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

// precacheEntry pulls the urls out of the shipped worker's precache manifest,
// which workbox emits as precacheAndRoute([{url:"...",revision:"..."},…]).
var precacheEntry = regexp.MustCompile(`url:"([^"]+)"`)

// TestThePrecacheManifestIsFetchableWithoutACredential is THE rule, and the one
// that has now been broken three times in three different ways.
//
// A service worker's install step fetches every URL in its precache manifest, and
// workbox throws `bad-precaching-response` if ANY of them comes back non-OK —
// deliberately, so a worker never activates holding a half-populated cache. So a
// single gated entry does not degrade the worker. It stops the worker from
// EXISTING.
//
// And the failure is invisible and self-perpetuating, which is why it survived two
// fixes. A logged-out client fetches the new sw.js happily — it is public, that was
// the last fix — starts installing it, hits the gated bundle, takes a 401, and
// throws the new worker away. The OLD worker stays in control, serving the OLD app,
// forever: every fix shipped after the client lost its cookie is unreachable BY THE
// CLIENT THAT NEEDS IT.
//
// It even hid behind a coincidence. A browser that still had its cookie installed
// the worker fine, so the panel healed on the desktop and never on the phone —
// which reads like a mobile bug, and is not one.
//
// So: whatever the worker precaches, the gate must let through. Assert it against
// the REAL mux and the REAL manifest, not against a list someone remembered to
// update.
func TestThePrecacheManifestIsFetchableWithoutACredential(t *testing.T) {
	srv := loginServer(t)

	matches := precacheEntry.FindAllStringSubmatch(swSource(t), -1)
	if len(matches) == 0 {
		t.Fatal("no precache manifest found in sw.js — the regexp is stale, not the worker")
	}

	seenBundle := false
	for _, m := range matches {
		url := m[1]
		if strings.HasPrefix(url, "assets/") {
			seenBundle = true
		}
		resp, err := srv.Client().Get(srv.URL + "/" + url)
		if err != nil {
			t.Fatalf("GET /%s: %v", url, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("the service worker precaches /%s, but a client with no credential gets %d for it.\n"+
				"That does not merely hide the file: workbox fails the whole INSTALL on a non-OK precache "+
				"response, so the new worker never activates and the stranded old one keeps control forever. "+
				"Serve it outside the gate, or stop precaching it.", url, resp.StatusCode)
		}
	}
	if !seenBundle {
		t.Error("the precache manifest names no assets/ bundle — this test would pass vacuously; " +
			"check that the build still fingerprints its output there")
	}
}

// TestTheAppShellStaysGated is the other half. Opening the bundle up must not open
// up the app: an unauthenticated navigation has to keep answering with the login
// form, which is the only reason the panel is reachable at all after a cookie dies.
func TestTheAppShellStaysGated(t *testing.T) {
	srv := loginServer(t)
	for _, p := range []string{"/", "/index.html"} {
		resp, err := srv.Client().Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s with no credential = %d, want 401: the shell is the gate, and a 401 is "+
				"what carries the login form", p, resp.StatusCode)
		}
	}
}

// TestServiceWorkerCachesLibraryMedia is the other kind of SW bug: not a rule
// broken, a route missing.
//
// Card portraits, scene backdrops and world covers all come from /media/, and the
// worker cached none of it — the precache globs sweep dist/ only, and the single
// runtime route matches navigations. So every portrait was a network request once
// the browser's own five-minute copy lapsed, which for a library of thirty
// characters is thirty requests each time it is opened, on the phone where the
// library is actually browsed.
//
// Gate the shipped artefact, like the rules above: the config that produces this
// has looked right while being wrong twice already.
func TestServiceWorkerCachesLibraryMedia(t *testing.T) {
	sw := swSource(t)
	if !strings.Contains(sw, "terva-media") {
		t.Error("sw.js registers no runtime cache for /media/: every card portrait is a " +
			"network request on every visit. Check the runtimeCaching entry in vite.config.ts.")
	}
	// Stale-while-revalidate specifically. CacheFirst would freeze a replaced
	// portrait for the whole expiration window: a card's id is derived from its
	// JSON, not its image, so changing the image keeps the URL.
	if !strings.Contains(sw, "StaleWhileRevalidate") {
		t.Error("the /media/ route is not StaleWhileRevalidate: a replaced portrait would be " +
			"served from cache until the entry expired, because its URL does not change")
	}
}

// TestLibraryMediaIsCachedAtRUNTIMEOnly is the standing rule applied to the route
// above, and the reason that route is legal at all.
//
// /media/ is behind the auth gate. A gated PRECACHE entry does not degrade the
// worker, it stops the worker from existing — install fetches every manifest URL
// with no credential and workbox discards the whole worker on a single non-OK
// response (see TestThePrecacheManifestIsFetchableWithoutACredential above, and
// the three times that bug shipped). A runtime route is a different thing
// entirely: it only ever stores the answer to a request the page itself made,
// credential included. So this asserts the distinction holds in the artefact.
func TestLibraryMediaIsCachedAtRuntimeOnly(t *testing.T) {
	for _, m := range precacheEntry.FindAllStringSubmatch(swSource(t), -1) {
		if strings.Contains(m[1], "media") {
			t.Errorf("sw.js PRECACHES %q: /media/ is gated, so a logged-out install takes a 401 "+
				"there and throws the entire worker away. It must be cached at runtime only.", m[1])
		}
	}
}
