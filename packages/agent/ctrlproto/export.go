package ctrlproto

import "context"

// Session export (sessions.export). Two audiences for one act: "markdown"
// renders the played scene as something a person reads, "tervasession" is the
// lossless transcript another terva can import. Both come back in the
// CardExport/WorldExport shape, so the client's download path is the one it
// already has.
//
// This is optional, like the doctor: a carrier serving a replayed or foreign
// session has no on-disk transcript to serialize, and answering "unsupported"
// is better than half-serving one.
type ExportController interface {
	SessionsExport(ctx context.Context, sess string, p SessionExportParams) (SessionExport, error)
}

// Export formats. Unknown values are refused rather than defaulted — a caller
// asking for a format we do not have wants an error, not a surprise file in a
// shape it cannot read.
const (
	// ExportMarkdown is the readable story: YAML front matter, then the scene
	// with each turn under its speaker. Lossy on purpose — no tool calls, no
	// reasoning, no unchosen variants.
	ExportMarkdown = "markdown"
	// ExportTervaSession is the raw JSONL round-trip (core.ExportSession), the
	// meaning tui-ctrlproto-parity reserved this verb for.
	ExportTervaSession = "tervasession"
)

// SessionExportParams names the format. Empty defaults to markdown: the verb
// exists because someone wants to read their story, and the round-trip format
// is the specialist ask.
type SessionExportParams struct {
	Format string `json:"format,omitempty"`
}

// SessionExport is a session serialized for download — the same triple
// cards.export and worlds.export return, so one client helper serves all three.
// Bytes is base64 on the wire.
type SessionExport struct {
	Filename string `json:"filename"`
	MimeType string `json:"mime_type"`
	Bytes    []byte `json:"bytes"`
}
