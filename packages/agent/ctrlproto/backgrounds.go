package ctrlproto

import "context"

// Scene backdrops on the wire — the last member of the content-library family.
// A background is just an image the Stage app renders behind a conversation, so
// there is no parsing or identity here; the only twist is that a background is
// BOUND per session (BackgroundBind writes SessionMeta.Background), unlike the
// global card/persona stores.
//
// BackgroundsController is OPTIONAL, like the other library controllers: a
// carrier without a background store simply does not implement it, so these verbs
// do not ripple to every WorkspaceService implementer.
type BackgroundsController interface {
	// BackgroundsList returns every stored backdrop.
	BackgroundsList(ctx context.Context) (BackgroundsListResult, error)
	// BackgroundsImport stores an image (by server path or raw bytes), detecting
	// the format from its bytes. Idempotent by content.
	BackgroundsImport(ctx context.Context, p BackgroundImportParams) (BackgroundView, error)
	// BackgroundsDelete removes a backdrop from the store.
	BackgroundsDelete(ctx context.Context, p BackgroundDeleteParams) error
	// BackgroundBind sets (or, with an empty id, clears) the backdrop bound to a
	// session. The session rides the frame, like the other session-scoped verbs.
	BackgroundBind(ctx context.Context, sess string, p BackgroundBindParams) error
	// BackgroundsGenerate paints a new backdrop from a text prompt (via the
	// session's configured image backend), stores it, and binds it to the session
	// in one step — generate-and-set. A bad request when no image backend is
	// configured.
	BackgroundsGenerate(ctx context.Context, sess string, p BackgroundGenerateParams) (BackgroundView, error)
}

// BackgroundImportParams carries an image to store; Bytes (an upload) wins over
// Path (a server-local file).
type BackgroundImportParams struct {
	Path  string `json:"path,omitempty"`
	Bytes []byte `json:"bytes,omitempty"`
}

// BackgroundDeleteParams names the backdrop to remove.
type BackgroundDeleteParams struct {
	ID string `json:"id"`
}

// BackgroundBindParams is the backdrop id to bind to the session, or "" to clear.
type BackgroundBindParams struct {
	Background string `json:"background"`
}

// BackgroundGenerateParams describes a scene to paint. Only Prompt is required;
// the rest map onto the image backend's own knobs (Backend picks among several,
// "" = the default).
type BackgroundGenerateParams struct {
	Prompt         string `json:"prompt"`
	NegativePrompt string `json:"negative_prompt,omitempty"`
	Size           string `json:"size,omitempty"`
	Backend        string `json:"backend,omitempty"`
}

// BackgroundsListResult is the payload of backgrounds.list.
type BackgroundsListResult struct {
	Backgrounds []BackgroundView `json:"backgrounds"`
}

// BackgroundView is one stored backdrop: its id and the media route to fetch it.
type BackgroundView struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}
