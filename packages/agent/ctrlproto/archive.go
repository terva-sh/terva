package ctrlproto

import "context"

// Archiving a session: the third thing you can do to one, between keeping it in
// the picker forever and destroying it.
//
// Until this existed, a directory's session list only ever grew, and the single
// way to shorten it was sessions.delete — which takes the transcript and its
// failure sidecar with it. Archiving moves the transcript into a compressed
// sub-directory that no listing walks; the session stops appearing everywhere
// terva shows sessions, and none of it is gone.
//
// SessionArchiveController is OPTIONAL, like the Stage controllers: a replay
// carrier serves a recording and has no directory to move anything into, so it
// answers CodeUnsupported rather than pretending.
type SessionArchiveController interface {
	// ArchiveSession compresses sess's transcript into the archive. The session
	// is closed first if it is live.
	ArchiveSession(ctx context.Context, sess string) (ArchivedSessionInfo, error)
	// ArchivedSessions lists what is in this directory's archive, newest first.
	ArchivedSessions(ctx context.Context) ([]ArchivedSessionInfo, error)
	// RestoreSession decompresses an archived transcript back into the sessions
	// directory, where every listing sees it again.
	RestoreSession(ctx context.Context, p RestoreSessionParams) (SessionInfo, error)
}

// ArchivedSessionInfo is one row in the archive browser.
//
// It carries the same descriptive fields a live session row does — title, model,
// message count — because an archive of opaque ids is one nobody restores from.
// The sizes are the two facts that only exist once archived, and they are what
// justify the compression to whoever is looking at the list.
type ArchivedSessionInfo struct {
	ID           string  `json:"id"`
	Title        string  `json:"title,omitempty"`
	Provider     string  `json:"provider,omitempty"`
	Model        string  `json:"model,omitempty"`
	MessageCount int     `json:"message_count,omitempty"`
	TotalCost    float64 `json:"total_cost,omitempty"`
	// Preview is the session's first user message, the same fallback the live
	// picker shows for an untitled session.
	Preview string `json:"preview,omitempty"`
	// Started is when the session began; ArchivedAt when it was archived. Both
	// RFC 3339, matching SessionInfo's other timestamps.
	Started    string `json:"started,omitempty"`
	ArchivedAt string `json:"archived_at,omitempty"`
	// Bytes on disk now, Original before compression.
	Bytes    int64 `json:"bytes,omitempty"`
	Original int64 `json:"original,omitempty"`
	// Experience/Card/World carry an immersive session's identity so Stage can
	// tell one archived chat from another the same way its Library does.
	Experience string `json:"experience,omitempty"`
	Card       string `json:"card,omitempty"`
	World      string `json:"world,omitempty"`
}

// ArchivedSessionsResult is the sessions.archived reply.
type ArchivedSessionsResult struct {
	Sessions []ArchivedSessionInfo `json:"sessions"`
}

// RestoreSessionParams names the archived session to bring back.
//
// The id travels in PARAMS, not in the frame's sess field, precisely because an
// archived session is not addressable: the frame's sess is a live handle that
// subscriptions and turn routing key on, and an id that resolves to no live
// session has no business being addressed as one.
type RestoreSessionParams struct {
	ID string `json:"id"`
}
