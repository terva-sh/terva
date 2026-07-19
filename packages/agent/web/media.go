//go:build terva_web

package web

import (
	"net/http"
	"strings"

	"terva.sh/terva/packages/agent/build"
)

// serveMedia streams library media by id: card avatars at /media/cards/<id>
// (backgrounds later, /media/backgrounds/<id>). It is a READ-only server for the
// content stores under $TERVA_HOME — never a general file endpoint — mounted
// under authMiddleware (so, like /ws, it is gated) and never precached (the PWA
// globs sweep only dist/), the two rules an avatar route must satisfy.
//
// The id is validated by the store (CardStore.AvatarPath returns "" for an id
// that could escape the library dir), so a "../.." can neither resolve nor be
// handed to http.ServeFile.
func serveMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	kind, id, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/media/"), "/")
	if !ok || id == "" {
		http.NotFound(w, r)
		return
	}
	var path string
	switch kind {
	case "cards":
		path = build.NewCardStore().AvatarPath(id)
	case "backgrounds":
		path = build.NewBackgroundStore().Path(id)
	default:
		http.NotFound(w, r)
		return
	}
	if path == "" {
		http.NotFound(w, r)
		return
	}
	// Private: the route is behind the auth gate, so a shared cache must not keep
	// a copy. Modest max-age — a delete+reimport can reuse a content-derived id.
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeFile(w, r, path)
}
