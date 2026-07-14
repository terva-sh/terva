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

// pwaShellPaths lists the embedded files served WITHOUT authentication.
//
// These are the files a browser needs to know terva is an installable app and to
// run its service worker: the manifest, the icons, and the worker scripts. None
// carries a secret — they are the same bytes for every deployment — and the app
// itself (index.html and the fingerprinted bundle under assets/) stays gated, so
// nothing about a session leaks.
//
// Leaving the worker behind the gate had a failure mode worth stating plainly: a
// logged-out client cannot fetch sw.js, so it cannot UPDATE sw.js, so a service
// worker that has stranded itself can never be replaced by a fixed one. The only
// cure is deleting the home-screen app — the app being the very thing that must
// keep working. An unauthenticated client can already see the login page; letting
// it also see an icon and a worker script concedes nothing further, and buys the
// installed app the ability to heal itself.
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
