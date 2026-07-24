package workspace

import (
	"context"
	"errors"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
)

// The archive group: the verb between keeping a session forever and destroying it.
//
// It mirrors DeleteSession's shape — close the live handle, move the file,
// broadcast — because the frontends treat the two as siblings and any difference
// in when a client learns the list changed would show up as a stale row.

var _ ctrlproto.SessionArchiveController = (*Workspace)(nil)

// ArchiveSession compresses a session's transcript into this directory's archive.
//
// The live handle is closed first, exactly as a delete does: archiving a session
// that still has an open writer would leave the writer appending to a file that
// is no longer in the sessions directory, and the appended turns would be lost
// with no error anywhere.
func (w *Workspace) ArchiveSession(_ context.Context, sess string) (ctrlproto.ArchivedSessionInfo, error) {
	if !validSessionID(sess) {
		return ctrlproto.ArchivedSessionInfo{}, ctrlproto.ErrNoSession
	}
	w.mu.Lock()
	if s, existed := w.sessions[sess]; existed {
		s.close()
		delete(w.sessions, sess)
	}
	w.mu.Unlock()

	a, err := core.ArchiveSession(w.root, w.cwd, sess)
	if err != nil {
		if errors.Is(err, core.ErrNoSuchSession) {
			return ctrlproto.ArchivedSessionInfo{}, ctrlproto.ErrNoSession
		}
		return ctrlproto.ArchivedSessionInfo{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", i18n.T("archive: %v", err))
	}
	// A session left the listing; every board and picker re-lists.
	w.BroadcastAll(ctrlproto.SessionsChangedEvent())
	return archivedInfo(a), nil
}

// ArchivedSessions lists this directory's archive, newest archived first.
func (w *Workspace) ArchivedSessions(_ context.Context) ([]ctrlproto.ArchivedSessionInfo, error) {
	list := core.ListArchivedSessions(w.root, w.cwd)
	out := make([]ctrlproto.ArchivedSessionInfo, 0, len(list))
	for _, a := range list {
		out = append(out, archivedInfo(a))
	}
	return out, nil
}

// RestoreSession brings an archived transcript back into the sessions directory.
//
// It does NOT open the session: restoring is a listing change, and which client
// (if any) then resumes it is that client's decision. Returning the SessionInfo
// lets a caller resume it immediately without a second round trip to find it.
func (w *Workspace) RestoreSession(ctx context.Context, p ctrlproto.RestoreSessionParams) (ctrlproto.SessionInfo, error) {
	if !validSessionID(p.ID) {
		return ctrlproto.SessionInfo{}, ctrlproto.ErrNoSession
	}
	_, err := core.RestoreSession(w.root, w.cwd, p.ID)
	switch {
	case errors.Is(err, core.ErrSessionNotArchived):
		return ctrlproto.SessionInfo{}, ctrlproto.Errorf(ctrlproto.CodeNoSession, "%s", i18n.T("no archived session %q", p.ID))
	case errors.Is(err, core.ErrSessionExists):
		return ctrlproto.SessionInfo{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("a session with that id is already active; it was left untouched"))
	case err != nil:
		return ctrlproto.SessionInfo{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "%s", i18n.T("restore: %v", err))
	}
	w.BroadcastAll(ctrlproto.SessionsChangedEvent())

	// Describe it from disk rather than opening it — an archived session is
	// usually restored to be looked at later, not resumed on the spot, and
	// opening one here would build an agent nobody asked for.
	info := ctrlproto.SessionInfo{ID: p.ID}
	for _, s := range core.DescribeSessions(w.root, w.cwd) {
		if build.SessionIDFromPath(s.Path) != p.ID {
			continue
		}
		info.Title = s.Title
		info.Provider = s.Provider
		info.Model = s.Model
		info.Experience = s.Experience
		break
	}
	return info, nil
}

func archivedInfo(a core.ArchivedSession) ctrlproto.ArchivedSessionInfo {
	out := ctrlproto.ArchivedSessionInfo{
		ID:           a.ID,
		Title:        a.Title,
		Provider:     a.Provider,
		Model:        a.Model,
		MessageCount: a.MessageCount,
		TotalCost:    a.TotalCost,
		Preview:      a.FirstUserText,
		Bytes:        a.Bytes,
		Original:     a.Original,
		Experience:   a.Experience,
		Card:         a.Card,
		World:        a.World,
	}
	if !a.Started.IsZero() {
		out.Started = a.Started.UTC().Format(time.RFC3339)
	}
	if !a.ArchivedAt.IsZero() {
		out.ArchivedAt = a.ArchivedAt.UTC().Format(time.RFC3339)
	}
	return out
}
