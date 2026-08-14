package ctrlproto

import "context"

// A "!" shell escape's result on the wire — stage 2 of
// docs/proposals/shell-escape-context.md.
//
// The escape is a CLIENT affordance: the TUI runs the command in its own
// process, in the user's working directory, and parks the output on screen. The
// agent is in the daemon and never sees any of it. This verb is how the client
// offers that output to the session, so the user's next message can be a
// question about what they just ran.
//
// Session-scoped (the session rides the frame) and served by an OPTIONAL
// controller, like note.bind above it, so the verb does not ripple out to every
// WorkspaceService implementer.
//
// GroupConversation rather than GroupControl, and the reason is worth stating
// because the block does reach the model: this verb grants strictly LESS than
// prompt, which is already in that group. A caller who can send a prompt can
// already put arbitrary text in front of the model, durably. This one appends
// an ephemeral block that survives a single request. Filing it under the
// higher-authority group would demand more authority for the weaker capability.
type ShellResultController interface {
	// ShellResult arms the session's next request with the result of a command
	// the user ran themselves. Replaces any result still waiting. An empty
	// Command disarms instead, so a client can withdraw an offer it has made.
	ShellResult(ctx context.Context, sess string, p ShellResultParams) error
}

// ShellResultParams carries one finished shell escape.
//
// Output is the RAW merged text, not the client's rendered block. The TUI
// styles its own copy with ANSI colour before painting it (renderShellBlock),
// and sending that would spend the model's attention on escape sequences and
// put terminal control codes in a provider request. The distinction is easy to
// get wrong and invisible once wrong, so the guard for it is on the client side
// where the two forms exist side by side.
type ShellResultParams struct {
	// Command is what the user typed after the "!", with no leading marker.
	Command string `json:"command"`
	// Output is the raw merged stdout/stderr, unstyled. The daemon bounds it —
	// a client is not trusted to have done so.
	Output string `json:"output"`
}
