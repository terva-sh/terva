//go:build terva_web

package web

import (
	"errors"
	"mime"
	"net/http"
	"os"
	"strings"

	"terva.sh/terva/packages/agent/attach"
)

// sharedPath is the download route for files an agent published with share_file.
// Mounted under authMiddleware, and NOT folded into /media/: that prefix is a
// read-only server for the library stores (card avatars, backgrounds) whose
// content terva itself produced, while this one serves bytes the AGENT chose,
// which is a different threat model and wants the headers below.
const sharedPath = "/shared/"

// inlineTypes is the closed set of content types this route will render in the
// browser rather than hand over as a download.
//
// It is an allowlist and not a kind check, because "is it an image" is the wrong
// question. image/svg+xml is an image by every classification in this codebase
// (attach.KindFor calls it one) and is also a script host: an SVG rendered inline
// executes JavaScript in this origin, which is where the session cookie lives and
// from which every control-plane verb is reachable. The same goes for text/html.
// An agent that has been talked into writing a hostile file must not be able to
// turn it into script execution simply by choosing an extension.
//
// Everything absent from this map downloads. That is the safe default and also
// the useful one: a file the user asked for is a file they want on disk.
// Every audio/video type attach's derivation can produce must appear here or be
// named in inline_coverage_test.go's declinedInline with a reason — the two
// tables live in different packages and drifting apart is how a shared .wav
// came to render nowhere. The guard is what makes that a failure rather than a
// discovery.
var inlineTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
	"image/avif": true,
	"audio/mpeg": true,
	"audio/mp4":  true,
	"audio/aac":  true,
	"audio/ogg":  true,
	"audio/wav":  true,
	"audio/webm": true,
	"audio/flac": true,
	"video/mp4":  true,
	"video/webm": true,
	"video/ogg":  true,
}

// sharedHandler streams one shared file: /shared/<session>/<id>.
//
// The store is injected rather than resolved in here, matching uploadHandler:
// which area a route serves is the mount's decision, and a test then drives a
// directory it owns instead of the process-wide $TERVA_HOME.
//
// Both path segments are required and neither is trusted. The store resolves an
// id only against its own session directory's entries, so a traversal in either
// resolves to nothing rather than to something interesting — this handler never
// builds a path from the request at all.
func sharedHandler(store *attach.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { serveShared(store, w, r) }
}

func serveShared(store *attach.Store, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sess, id, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, sharedPath), "/")
	if !ok || sess == "" || id == "" {
		http.NotFound(w, r)
		return
	}
	ref, err := store.Resolve(sess, id)
	if err != nil {
		// Expired, never shared, or another session's — all one answer. A
		// swept file is the normal case, not an error worth distinguishing.
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(ref.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r) // swept between the resolve and the open
			return
		}
		http.Error(w, "could not read the shared file", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "could not read the shared file", http.StatusInternalServerError)
		return
	}

	h := w.Header()
	// The type is DERIVED from what landed on disk (extension, then a sniff of
	// the leading bytes), never declared by whoever produced the file. Combined
	// with nosniff, that means the browser treats the response as exactly the
	// one thing this daemon is willing to call it.
	ctype := ref.Mime
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	h.Set("Content-Type", ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	// A per-response CSP replacing the app's: this body is not the app, and
	// nothing it contains should be able to reach anything. `sandbox` alone
	// drops it into an opaque origin even if it does end up being rendered.
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'")
	h.Set("Content-Disposition", contentDisposition(ref.Name, inlineWanted(r) && inlineTypes[ctype]))
	// Private because the route is gated; immutable because an id names one set
	// of bytes forever — Publish mints a new id rather than overwriting.
	h.Set("Cache-Control", "private, max-age=3600, immutable")
	// ServeContent, not io.Copy: <audio> and <video> seek with Range requests,
	// and a player that cannot seek is a player that looks broken.
	http.ServeContent(w, r, ref.Name, info.ModTime(), f)
}

// inlineWanted reports the client asking for a renderable response rather than a
// download. Opt-in, so the default for every request — including one typed into
// the address bar — is the download.
func inlineWanted(r *http.Request) bool {
	switch r.URL.Query().Get("inline") {
	case "1", "true":
		return true
	}
	return false
}

// contentDisposition builds the header, filename included.
//
// mime.FormatMediaType handles the RFC 5987 encoding of a non-ASCII name and,
// more importantly, the quoting of one containing a quote or a semicolon — the
// characters that would otherwise let a filename forge extra header parameters.
// A name that somehow defeats it falls back to the bare disposition: an
// unhelpful download beats a forged header.
func contentDisposition(name string, inline bool) string {
	kind := "attachment"
	if inline {
		kind = "inline"
	}
	if formatted := mime.FormatMediaType(kind, map[string]string{"filename": name}); formatted != "" {
		return formatted
	}
	return kind
}
