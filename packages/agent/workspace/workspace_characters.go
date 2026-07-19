package workspace

import (
	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
)

// The "Characters" surface — the content library as a workspace pane. It is a
// read view over the same stores the cards.*/personas.* verbs mutate, reusing
// their summaries; a controller renders it to browse and pick. Mutation stays on
// the dedicated verbs, so the pane carries no actions — but it is Live: every
// library write broadcasts SurfaceUpdatedEvent("characters") so an open pane
// refreshes (see broadcastLibraryChanged).

// charactersView builds the library pane: every stored card plus the full
// persona roster, projected through the same summaries the verbs return.
func (s *wsSession) charactersView() *ctrlproto.CharactersView {
	v := &ctrlproto.CharactersView{}
	if stored, err := build.NewCardStore().List(); err == nil {
		for _, sc := range stored {
			v.Cards = append(v.Cards, cardSummary(sc))
		}
	}
	for _, p := range build.AllPersonas() {
		v.Personas = append(v.Personas, personaSummary(p))
	}
	return v
}

// broadcastLibraryChanged tells subscribers the workspace-global library moved,
// so an open Characters pane re-fetches. Cards and personas live under
// $TERVA_HOME (not one session), so this is a workspace broadcast.
func (w *Workspace) broadcastLibraryChanged() {
	w.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("characters"))
}
