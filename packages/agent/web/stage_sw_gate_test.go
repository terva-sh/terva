//go:build terva_web

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stageServer is a mux with Stage enabled and a token configured, so gated paths
// answer 401 without a credential while the ungated /stage/ shell files serve
// either way. It mirrors loginServer, which does not enable Stage.
func stageServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(newMux(context.Background(), newFakeWS(), Options{Token: "s3cret", AllowStage: true}))
	t.Cleanup(srv.Close)
	return srv
}

// TestStagePrecacheFetchableWithoutACredential is
// TestThePrecacheManifestIsFetchableWithoutACredential for the Stage scope — and
// it is the test whose absence let a real trap ship.
//
// Stage registers the single shipped service worker at scope /stage/: the plugin's
// registerSW.js resolves ./sw.js relative to the /stage/ page, so the worker lives
// at /stage/sw.js and its RELATIVE precache entries resolve to /stage/assets/…,
// /stage/manifest.webmanifest, /stage/pwa-*.png, … A logged-out install fetches
// every one of them, and workbox fails the whole install on any non-OK response —
// so a single gated entry does not degrade the Stage worker, it stops it from
// EXISTING, stranding the old one (and the old app) forever. Every /stage/ precache
// URL must therefore be reachable with no credential.
func TestStagePrecacheFetchableWithoutACredential(t *testing.T) {
	srv := stageServer(t)

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
		// The Stage worker lives at /stage/sw.js, so its relative precache URLs
		// resolve against /stage/, not the root.
		resp, err := srv.Client().Get(srv.URL + "/stage/" + url)
		if err != nil {
			t.Fatalf("GET /stage/%s: %v", url, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("the Stage service worker precaches /stage/%s, but a client with no credential gets %d.\n"+
				"workbox fails the whole INSTALL on a non-OK precache response, so the new Stage worker never "+
				"activates and the stranded old one keeps control forever. Serve it outside the gate "+
				"(stagePwaShellPaths / the /stage/assets/ mount), or stop precaching it.", url, resp.StatusCode)
		}
	}
	if !seenBundle {
		t.Error("the precache manifest names no assets/ bundle — this test would pass vacuously; " +
			"check that the build still fingerprints its output there")
	}
}

// TestStageShellStaysGated is the other half: opening the bundle up must not open
// up the app. An unauthenticated request for the Stage shell — the app root and a
// client route under it — has to keep answering 401, which is what carries the
// login form and the only reason Stage is reachable at all after a cookie dies.
func TestStageShellStaysGated(t *testing.T) {
	srv := stageServer(t)
	for _, p := range []string{"/stage/", "/stage/library"} {
		resp := authedGet(t, srv.URL+p, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s with no credential = %d, want 401: the Stage shell is gated, and a 401 "+
				"is what carries the login form", p, resp.StatusCode)
		}
	}
}

// TestStageShellNotPrecached — stage.html must never enter the precache manifest,
// for the same reason index.html must not (TestServiceWorkerDoesNotPrecacheTheShell):
// a precached, cache-first shell answers a /stage/ navigation itself and buries the
// daemon's 401 login form. The html glob exclusion already guarantees it; this pins
// it so a future workbox tweak cannot quietly reintroduce it.
func TestStageShellNotPrecached(t *testing.T) {
	sw := swSource(t)
	for _, entry := range []string{`url:"stage.html"`, `url: "stage.html"`, `"stage.html"`} {
		if strings.Contains(sw, entry) {
			t.Fatalf("sw.js precaches the Stage shell (%s): a cache-first precached shell answers a "+
				"/stage/ navigation and hides the login form. Keep `html` out of workbox.globPatterns.", entry)
		}
	}
}
