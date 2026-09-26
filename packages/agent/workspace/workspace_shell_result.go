package workspace

// The daemon half of shell.result — stage 2 of
// docs/proposals/shell-escape-context.md.
//
// A client ran a "!" command in its own process and offers the output to this
// session. All this does is hand it to the session's shell-result slot, which
// decides how it rides (core/shellresult: the ephemeral tail, once, restored if
// the prompt that carried it is withdrawn).
//
// Optional controller, so the verb does not ripple to the other
// WorkspaceService implementers.

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/i18n"
)

var _ ctrlproto.ShellResultController = (*Workspace)(nil)

// ShellResult arms the session's next request with the result of a command the
// user ran themselves.
func (w *Workspace) ShellResult(_ context.Context, sess string, p ctrlproto.ShellResultParams) error {
	s, err := w.resolve(sess)
	if err != nil {
		return err
	}
	if s.agent == nil {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("this session has no agent to show a shell result to"))
	}
	asm := build.AssemblerOf(s.agent)
	if asm == nil {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("this session has no agent to show a shell result to"))
	}
	slot := asm.ShellResult()

	cmd := strings.TrimSpace(p.Command)
	if cmd == "" {
		// A disarm, not an error. A client that ran a command and then decided
		// not to offer it says so this way, and a client that never armed
		// anything gets a harmless no-op rather than a failure to handle.
		slot.Set("", "")
		return nil
	}

	// Handed over whole. The slot bounds what rides and what is held, in one
	// place, without materialising a []rune of it — a second cap here would be
	// policy that can drift from that one, and it would save nothing: the
	// frame is already read and decoded by the time this runs.
	slot.Set(cmd, p.Output)
	return nil
}
