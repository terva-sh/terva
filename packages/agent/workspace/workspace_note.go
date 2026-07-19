package workspace

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// The author's note on the wire. note.set writes SessionMeta.Note (a durable,
// last-wins meta row) AND updates the live build.NoteRecord that the per-turn
// tail reads, then broadcasts a snapshot so the open steering view re-renders.
// The two writes keep the persisted value and the live-injected value in step;
// the note takes effect on the next turn with no cache bust. Optional controller,
// so the verb does not ripple to the other WorkspaceService implementers.
var _ ctrlproto.NoteController = (*Workspace)(nil)

// NoteSet sets (or, with an empty string, clears) a session's author's note.
// Only immersive sessions carry a note record; a coding session is a bad request.
func (w *Workspace) NoteSet(_ context.Context, sess string, p ctrlproto.NoteSetParams) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	if s.note == nil {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "the author's note is only available for chat/play sessions")
	}
	text := strings.TrimSpace(p.Text)
	if err := s.sess.SetNote(text); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "set note: %v", err)
	}
	s.note.Set(text)
	s.broadcast(ctrlproto.SnapshotEvent(s.snapshot()))
	return nil
}
