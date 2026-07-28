package attach

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"terva.sh/terva/packages/agent/config"
)

// ShareDirName is the outbound area's name under $TERVA_HOME.
//
// Unlike DirName this is NOT handed to packages/agent/build as a sandbox root.
// The agent publishes through the share_file tool, which calls Publish; it never
// writes here itself, and granting it a read would let a jailed session
// enumerate every other session's deliverables for nothing in return. The
// refusal is asserted in build's sandbox_roots_test.go, alongside auth.json and
// sessions/ — the company it belongs in.
const ShareDirName = "shared"

const (
	// ShareTTL is how long a shared file stays downloadable — seven times the
	// inbound TTL, because the two ends of the pipe are not symmetric. An
	// uploaded file has done its job the moment the agent has read it; a shared
	// one IS the deliverable, and the obvious way to want it is to reopen the
	// session days later and click the thing the agent made.
	ShareTTL = 7 * 24 * time.Hour

	// ShareCapBytes bounds the whole share area. Same figure as the inbound cap:
	// the backstop is about not letting a runaway agent fill the disk, and that
	// concern does not care which direction the files were going.
	ShareCapBytes int64 = 2 << 30

	// ShareGrace protects a just-published file from cap eviction, so a turn that
	// shares several large files cannot evict the ones it shared moments ago —
	// the user has not even seen the transcript yet.
	ShareGrace = time.Hour
)

// SharePolicy is the outbound area's retention rule.
var SharePolicy = Policy{TTL: ShareTTL, CapBytes: ShareCapBytes, Grace: ShareGrace}

// ErrNotRegular reports a source that is a directory, a device, or anything else
// there is no sensible way to hand a browser.
var ErrNotRegular = errors.New("not a regular file")

// NewShareStore returns the outbound store at $TERVA_HOME/shared.
func NewShareStore() *Store {
	return NewShareStoreAt(filepath.Join(config.TervaHome(), ShareDirName))
}

// NewShareStoreAt returns an outbound store rooted at an explicit directory.
func NewShareStoreAt(root string) *Store {
	return &Store{
		root:     root,
		label:    "shared file",
		idPrefix: "shr_",
		policy:   SharePolicy,
		maxBytes: MaxBytes,
	}
}

// Publish copies the file at src into the session's share dir and returns the
// Ref naming it. name relabels the file for the user; empty keeps src's base.
//
// It COPIES. Hardlinking the source would be free and is the obvious
// optimization, but it would also mean an agent that edits the file in place
// afterwards retroactively changes what it already handed the user — and a
// download link that silently serves different bytes than the transcript
// described is worse than no link. Immutability per id is the point of the area.
//
// The stat below is a courtesy that fails a too-large file before writing 100 MB
// of it; Stage still bounds the stream, which is what actually holds when a file
// grows between the stat and the copy.
func (s *Store) Publish(sess, src, name string) (Ref, error) {
	info, err := os.Stat(src)
	if err != nil {
		return Ref{}, err
	}
	if !info.Mode().IsRegular() {
		return Ref{}, fmt.Errorf("%s: %w", src, ErrNotRegular)
	}
	if info.Size() > s.maxBytes {
		return Ref{}, ErrTooLarge
	}
	f, err := os.Open(src)
	if err != nil {
		return Ref{}, err
	}
	defer f.Close()
	if name == "" {
		name = filepath.Base(src)
	}
	return s.Stage(sess, name, f)
}
