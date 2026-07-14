//go:build terva_web

package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// clientFS holds the built control-panel PWA. The `all:` prefix embeds files
// whose names begin with `_` or `.` too, matching what a bundler may emit.
//
//go:embed all:client/dist
var clientFS embed.FS

// pwaShellPaths lists the embedded top-level files served WITHOUT authentication:
// the manifest, the icons, and the worker scripts. See also assetsPrefix, which
// is the same rule applied to the fingerprinted bundle.
//
// THE RULE, and it is the one that has been broken three times: NOTHING THE
// SERVICE WORKER PRECACHES MAY BE BEHIND THE AUTH GATE.
//
// A worker's install step fetches every URL in its precache manifest, and workbox
// throws `bad-precaching-response` if ANY of them comes back non-OK — deliberately,
// so a worker never activates holding a half-populated cache. So one gated entry in
// the manifest does not degrade the worker; it stops the worker from EXISTING.
//
// That is the trap, because the failure is invisible and self-perpetuating. A
// logged-out client fetches the new sw.js happily (it is public — that was the last
// fix), starts installing it, hits the gated bundle, takes a 401, and throws the new
// worker away. The OLD worker stays in control, serving the OLD app, forever. Every
// fix shipped after the client lost its cookie is unreachable BY THE CLIENT THAT
// NEEDS IT, and the only cure is deleting the home-screen app — the app being the
// thing that must keep working.
//
// It hid behind a coincidence, too: on a browser that still had its cookie the
// install fetched the bundle fine, so the panel healed on the desktop and never on
// the phone, which reads like a mobile bug and is not one.
//
// So the bundle is served outside the gate. It concedes nothing: it is the same
// bytes for every deployment, it is published in every release, and it holds no
// session, no token, and no workspace. index.html STAYS gated — that is what makes
// an unauthenticated navigation answer with the login form instead of an app that
// cannot talk to anything.
//
// The list is computed from the build output rather than hard-coded because the
// workbox runtime ships under a content-hashed name (workbox-9c191d2f.js), which
// changes whenever the toolchain does.
func pwaShellPaths() []string {
	sub, err := fs.Sub(clientFS, "client/dist")
	if err != nil {
		return nil
	}
	ents, err := fs.ReadDir(sub, ".")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		switch {
		case n == "sw.js", n == "registerSW.js", n == "manifest.webmanifest",
			strings.HasPrefix(n, "favicon."),
			strings.HasPrefix(n, "apple-touch-icon"),
			strings.HasPrefix(n, "pwa-"),
			strings.HasPrefix(n, "workbox-") && strings.HasSuffix(n, ".js"):
			out = append(out, "/"+n)
		}
	}
	return out
}

// assetsPrefix is where the bundler emits the fingerprinted JS and CSS, and it is
// the bulk of what the service worker precaches. Served outside the gate for the
// reason spelled out on pwaShellPaths: a precache entry the client cannot fetch is
// a service worker that cannot install.
//
// Safe to expose as a PREFIX (rather than a file list) because shellHandler stats
// the path first and 404s anything that is not a real embedded file — and because
// everything under it is content-addressed build output. index.html does not live
// here; it stays behind the gate.
const assetsPrefix = "/assets/"

// shellHandler serves one embedded file and nothing else. It deliberately does
// NOT share staticHandler's SPA fallback: these routes are outside the auth gate,
// and a fallback there would hand index.html to an unauthenticated request for
// any misspelled shell path — turning a 404 into a way around the gate.
func shellHandler() http.Handler {
	sub, err := fs.Sub(clientFS, "client/dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, statErr := fs.Stat(sub, strings.TrimPrefix(r.URL.Path, "/")); statErr != nil {
			http.NotFound(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// staticHandler serves the embedded PWA with SPA fallback: a request for a path
// that is not a real asset gets index.html, so client-side routing works.
func staticHandler() http.Handler {
	sub, err := fs.Sub(clientFS, "client/dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	files := http.FileServer(http.FS(sub))
	index, ierr := fs.ReadFile(sub, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, statErr := fs.Stat(sub, p); statErr != nil {
			// Unknown path → SPA fallback to index.html.
			if ierr != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(index)
			return
		}
		files.ServeHTTP(w, r)
	})
}
