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
		if n := e.Name(); isPWAShellFile(n) {
			out = append(out, "/"+n)
		}
	}
	return out
}

// isPWAShellFile reports whether a top-level dist file name is part of the
// unauthenticated PWA shell — the manifest, the icons, and the worker scripts.
// Shared by pwaShellPaths (root) and appShellPaths (each app's prefix) so the
// ungated allowlists cannot drift apart as the toolchain renames things (the
// workbox runtime ships under a content-hashed name).
func isPWAShellFile(n string) bool {
	switch {
	case n == "sw.js", n == "registerSW.js", n == "manifest.webmanifest",
		strings.HasPrefix(n, "favicon."),
		strings.HasPrefix(n, "apple-touch-icon"),
		strings.HasPrefix(n, "pwa-"),
		strings.HasPrefix(n, "workbox-") && strings.HasSuffix(n, ".js"):
		return true
	}
	return false
}

// A second app is a page of its own in the same build: Stage at /stage/, and
// the Talkoot app at /talkoot/. Each one is mounted the same way, by mountApp,
// so the rules below hold for every app and cannot drift apart between them.
//
// appShellPaths is pwaShellPaths for an app mounted at prefix: the same shell
// files, served without auth under the prefix instead of at root, PLUS the
// app's own manifest. The app registers the (single, shared) service worker at
// scope prefix/, so its precache entries resolve to prefix-relative URLs, and,
// exactly as at root, a precache entry the client cannot fetch is a worker that
// cannot install. So the app's shell files sit outside the gate too. Only the
// app's HTML (the shell itself) stays gated, which is what makes an
// unauthenticated navigation to prefix/ answer with the login form. See
// appShellHandler and TestStagePrecacheFetchableWithoutACredential.
func appShellPaths(prefix, manifest string) []string {
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
		if n := e.Name(); isPWAShellFile(n) || n == manifest {
			out = append(out, prefix+"/"+n)
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

// appShellHandler serves one embedded shell file of an app, mapped from a
// prefix/ URL onto the dist root, and nothing else. Like shellHandler, and
// unlike appHandler, it sits OUTSIDE the auth gate and deliberately does NOT
// SPA-fall-back: a fallback here would hand the app's HTML to an
// unauthenticated request for any misspelled path, turning a 404 into a way
// around the gate. It backs prefix/assets/… and the shell files (sw.js,
// registerSW.js, workbox-*, the manifests, icons) that the app's service worker
// precaches, which must be fetchable without a credential.
func appShellHandler(prefix string) http.Handler {
	sub, err := fs.Sub(clientFS, "client/dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	stripped := http.StripPrefix(prefix, http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, statErr := fs.Stat(sub, strings.TrimPrefix(r.URL.Path, prefix+"/")); statErr != nil {
			http.NotFound(w, r)
			return
		}
		stripped.ServeHTTP(w, r)
	})
}

// appHandler serves the gated SHELL of an app: prefix/ (the app's HTML) and any
// unknown prefix/<route> (the same HTML, as an SPA fallback). It sits behind
// the auth gate, so an unauthenticated navigation answers with the login form,
// exactly like index.html.
//
// The app's BUNDLE and shell files are served separately and OUTSIDE the gate
// by appShellHandler, mounted ahead of this on more specific prefixes, because
// the prefix/-scoped service worker precaches them. Those specific routes win
// ServeMux's longest-prefix match, so a real bundle file never reaches here.
// With base './', the HTML at prefix/ references ./assets/x, which resolves to
// prefix/assets/x and is served ungated by appShellHandler.
func appHandler(prefix, page string) http.Handler {
	sub, err := fs.Sub(clientFS, "client/dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	stripped := http.StripPrefix(prefix, http.FileServer(http.FS(sub)))
	html, herr := fs.ReadFile(sub, page)
	writeShell := func(w http.ResponseWriter) {
		if herr != nil {
			http.Error(w, page+" is not built", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(html)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, prefix+"/")
		if p == "" {
			writeShell(w) // the app root
			return
		}
		if _, statErr := fs.Stat(sub, p); statErr != nil {
			writeShell(w) // a client route → SPA fallback
			return
		}
		stripped.ServeHTTP(w, r) // a real embedded file (assets/*, the HTML, …)
	})
}

// mountApp mounts an app at prefix on mux: its shell files and its bundle
// outside the gate, and its HTML and client routes behind it. The specific
// routes are registered ahead of the gated prefix/ catch-all, and ServeMux's
// longest-prefix rule makes them win.
func mountApp(mux *http.ServeMux, opts Options, prefix, page, manifest string) {
	for _, p := range appShellPaths(prefix, manifest) {
		mux.Handle(p, securityHeaders(appShellHandler(prefix)))
	}
	mux.Handle(prefix+"/assets/", securityHeaders(appShellHandler(prefix)))
	mux.Handle(prefix+"/", authMiddleware(opts, securityHeaders(appHandler(prefix, page))))
}

// The Stage app: stage.html at /stage/, with its own stage.webmanifest.
const (
	stagePrefix   = "/stage"
	stagePage     = "stage.html"
	stageManifest = "stage.webmanifest"
)

// The Talkoot app: talkoot.html at /talkoot/, with its own talkoot.webmanifest.
const (
	talkootPrefix   = "/talkoot"
	talkootPage     = "talkoot.html"
	talkootManifest = "talkoot.webmanifest"
)

// stagePwaShellPaths is the ungated shell of Stage, which the supervisor's
// precache test walks.
func stagePwaShellPaths() []string { return appShellPaths(stagePrefix, stageManifest) }

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
