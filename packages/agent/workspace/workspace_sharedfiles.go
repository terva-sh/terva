package workspace

import (
	"context"
	"errors"
	"os"
	"time"

	"terva.sh/terva/packages/agent/attach"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
)

// The read side of share_file: what this session handed to the user, and the
// bytes behind one of those handles.
//
// The web carrier serves the same files over an HTTP route it mounts, which is
// why the panel could always render them. These verbs are for every OTHER
// carrier — a native client on a unix socket has no route to fetch from, so
// without them the TUI could show that a share happened and nothing more.
//
// Both verbs are scoped to the frame's session and go no wider. attach.Store
// resolves an id only against that session's own directory, so a caller cannot
// name another conversation's deliverables even by guessing an id correctly.

var _ ctrlproto.SharedFilesController = (*Workspace)(nil)

// SharedFiles lists what this session published, newest first.
//
// A session that shared nothing is an empty list, not an error — and neither is
// one whose files the sweeper has already taken. "This conversation produced no
// deliverables" is an ordinary state, and a client renders it as an empty
// drawer rather than as a failure.
//
// The listing is built from the STORE, not from the transcript. The two can
// disagree: the sweeper reaps bytes while the message that named them lives on
// (the card stays, deliberately, saying what was shared). A drawer offering
// actions must reflect what is actually on disk, or every action in it is a
// promise the daemon cannot keep.
func (w *Workspace) SharedFiles(_ context.Context, sess string) ([]ctrlproto.SharedFileEntry, error) {
	if !validSessionID(sess) {
		return nil, ctrlproto.ErrNoSession
	}
	if w.shared == nil {
		return nil, ctrlproto.Errorf(ctrlproto.CodeUnsupported, "%s", i18n.T("this host has no share store"))
	}
	refs, err := w.shared.List(sess)
	if err != nil {
		return nil, ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", i18n.T("shared files: %v", err))
	}
	out := make([]ctrlproto.SharedFileEntry, 0, len(refs))
	for _, ref := range refs {
		out = append(out, sharedEntry(ref))
	}
	return out, nil
}

// SharedFileFetch returns one shared file's bytes.
//
// Bounded by ctrlproto.MaxSharedFetchBytes, and the bound is checked against
// what is ON DISK before the read rather than after: the point is not to load
// a gigabyte into memory and then decline to send it. A caller that hits the
// bound still has the path (same host) or the web route (any host), which is
// what the error says.
func (w *Workspace) SharedFileFetch(_ context.Context, sess string, p ctrlproto.SharedFileRef) (ctrlproto.SharedFileContent, error) {
	if !validSessionID(sess) {
		return ctrlproto.SharedFileContent{}, ctrlproto.ErrNoSession
	}
	if w.shared == nil {
		return ctrlproto.SharedFileContent{}, ctrlproto.Errorf(ctrlproto.CodeUnsupported, "%s", i18n.T("this host has no share store"))
	}
	if p.ID == "" {
		return ctrlproto.SharedFileContent{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("no shared file id"))
	}
	ref, err := w.shared.Resolve(sess, p.ID)
	if err != nil {
		// Expired, never shared, or another session's — one answer for all
		// three, as the web route gives. A swept file is the normal case and
		// distinguishing the others would confirm ids to whoever guessed them.
		return ctrlproto.SharedFileContent{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("no such shared file"))
	}
	info, err := os.Stat(ref.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ctrlproto.SharedFileContent{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("no such shared file"))
		}
		return ctrlproto.SharedFileContent{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", i18n.T("shared file: %v", err))
	}
	if info.Size() > ctrlproto.MaxSharedFetchBytes {
		return ctrlproto.SharedFileContent{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s",
			i18n.T("%s is too large to fetch over the control plane (%d bytes, limit %d) — open it from its path instead",
				ref.Name, info.Size(), ctrlproto.MaxSharedFetchBytes))
	}
	data, err := os.ReadFile(ref.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ctrlproto.SharedFileContent{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%s", i18n.T("no such shared file"))
		}
		return ctrlproto.SharedFileContent{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", i18n.T("shared file: %v", err))
	}
	return ctrlproto.SharedFileContent{ID: ref.ID, Name: ref.Name, Mime: ref.Mime, Data: data}, nil
}

// sharedEntry converts a store Ref into the wire row.
//
// The embedded core.SharedFile is deliberately the same shape the transcript
// carries, so a client joins a listing row to a card by id with nothing to
// convert. CallID is absent here and that is correct: the store knows which
// session a file belongs to, not which tool call produced it — that fact lives
// on the message, which is where the card reads it.
func sharedEntry(ref attach.Ref) ctrlproto.SharedFileEntry {
	out := ctrlproto.SharedFileEntry{
		SharedFile: core.SharedFile{
			ID:   ref.ID,
			Name: ref.Name,
			Kind: ref.Kind,
			Mime: ref.Mime,
			Size: ref.Size,
		},
		Path: ref.Path,
	}
	if !ref.ExpiresAt.IsZero() {
		out.ExpiresAt = ref.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return out
}
