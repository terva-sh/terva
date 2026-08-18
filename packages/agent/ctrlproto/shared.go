package ctrlproto

import (
	"context"

	"terva.sh/terva/packages/core"
)

// The files an agent handed to the user, and how a client gets at them.
//
// share_file publishes into a per-session area and the record rides the tool
// result, so a client can already RENDER what was shared from the transcript
// alone. What it cannot do from the transcript is act on any of it: the record
// is a handle (`shr_…`), not bytes and not a path. The web panel resolves that
// handle over an HTTP route it mounts; a native client on a unix socket has no
// such route, which is why the TUI showed a tool call and nothing else.
//
// These two verbs are that missing resolution, for every carrier. They are in
// the SESSION group and read-only: a share is something the agent already
// decided to hand over, and reading back what you were given confers no
// authority the transcript did not.
//
// SharedFilesController is OPTIONAL. A replay carrier has no store behind it
// and answers CodeUnsupported rather than claiming the session shared nothing —
// the two are different answers and a client shows different things for them.
type SharedFilesController interface {
	// SharedFiles lists what this session published, newest first. A session
	// that shared nothing returns an empty list, not an error.
	SharedFiles(ctx context.Context, sess string) ([]SharedFileEntry, error)
	// SharedFileFetch returns one shared file's bytes, so a client can preview
	// it, save it to its own disk, or hand it to a viewer. It is the ONLY way a
	// remote client can reach the content: Path in the listing names the
	// daemon's filesystem, not the client's.
	SharedFileFetch(ctx context.Context, sess string, p SharedFileRef) (SharedFileContent, error)
}

// MaxSharedFetchBytes bounds one shared.fetch response.
//
// The share store accepts files far larger than this (a video, a database
// dump). Those are fine to LIST and fine to serve over HTTP with range
// requests; they are not fine to inline in a single control frame, which is
// read into memory whole at both ends. A client that wants a file past this
// bound has the path (same host) or the web route (any host).
//
// The cap is the daemon's, not the transport's: it applies in-process too, so
// the failure is the same one everywhere rather than one that appears only
// after someone connects over a socket.
const MaxSharedFetchBytes int64 = 8 << 20

// MaxFrameBytes caps a single inbound WebSocket message so one oversized frame
// cannot exhaust memory. It lives HERE, in the protocol package, because both
// ends must agree on it and neither end owns it.
//
// It used to be declared twice — once in web/conn.go for the server's read
// limit, once in ctrlclient/ws.go for the client's — kept equal by a comment
// asking a human to keep them in step. That is not a mechanism. A client limit
// below the server's silently truncates a legitimate snapshot into a dead
// connection, and the browser sees only a closed socket with nothing naming the
// size, which is exactly how the previous 16 MiB ceiling was diagnosed.
//
// Note the 4/3 base64 inflation when sizing this against a file: an upload of N
// bytes needs a frame of ~1.34N plus JSON overhead. At 16 MiB the real ceiling
// on an attachment was ~12 MB of file, and a 19.6 MB character card (a real one
// off chub.ai — a 7680x2160 portrait) produced a 25.0 MiB frame gorilla killed
// the connection over.
const MaxFrameBytes = 32 << 20 // 32 MiB

// MaxUploadFileBytes is the largest FILE a client should attempt to send in one
// frame — MaxFrameBytes discounted by base64 inflation, with headroom for the
// surrounding JSON. Derived rather than typed, so the two cannot drift.
const MaxUploadFileBytes = MaxFrameBytes / 4 * 3 // ~24 MiB of file

// SharedFileEntry is one row of a session's share listing: the record the
// transcript already carries, plus where the bytes are on the DAEMON's disk.
//
// Path is host-local and may be empty. It is what lets a client on the daemon's
// own machine (the TUI, terva attach to localhost) open a file in the system
// viewer or put a real path on the clipboard, which is the difference between a
// listing you can look at and one you can use. A client that is not on that
// host must ignore it and fetch instead — a path from another machine is at
// best useless and at worst names a different file with the same name.
type SharedFileEntry struct {
	core.SharedFile
	Path string `json:"path,omitempty"`
}

// SharedFileRef names one shared file to act on.
type SharedFileRef struct {
	ID string `json:"id"`
}

// SharedFileContent is one shared file's bytes and what they are.
//
// Data is raw and rides the frame as base64 (encoding/json's []byte). Name and
// Mime travel with it so a client can write the file out, or choose a renderer,
// without joining back to the listing it may not have fetched.
type SharedFileContent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mime string `json:"mime,omitempty"`
	Data []byte `json:"data,omitempty"`
}

// SharedFilesResult is the payload of a [MethodSharedList] response.
type SharedFilesResult struct {
	Files []SharedFileEntry `json:"files"`
}
