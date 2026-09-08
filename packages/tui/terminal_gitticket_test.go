package tui

// *ProcTerm must keep satisfying git-ticket's tui.Terminal interface.
//
// This is not decoration. git-ticket's TUI entry point is
// view.Run(term tui.Terminal, ...), and terva's plan for a /ticket
// command inside the interactive TUI (TKT-01M1Z52DHW) hands terva's own
// terminal to it. That works today with no adapter because the two
// Terminal interfaces declare identical methods.
//
// The assertion below is what makes that a checked fact instead of a
// coincidence. It fails the moment somebody changes a method on terva's
// Terminal, and the obvious change is the tempting one: giving OnResize
// a detach return. That is exactly why ProcTerm.OnResizeDetach is a
// separate method.
//
// If this file stops compiling, the fix is not to delete it. Either
// keep the signatures aligned, or accept that the /ticket handoff needs
// an adapter and say so on TKT-01M1Z52DHW first.

import (
	"testing"

	gttui "github.com/terva-sh/git-ticket/tui"
)

var _ gttui.Terminal = (*ProcTerm)(nil)

// *HandoffTerm is the value the /ticket handoff actually passes to
// view.Run, so it has to satisfy the same interface. It wraps a
// Terminal and overrides two methods, and this catches an override
// whose signature drifts from the one it means to replace.
var _ gttui.Terminal = (*HandoffTerm)(nil)

// TestProcTermSatisfiesGitTicketTerminal exists so the assertion above
// has a named home in the test output. The compile-time check is the
// real gate; this reports it as a passing test rather than as silence.
func TestProcTermSatisfiesGitTicketTerminal(t *testing.T) {
	var term gttui.Terminal = NewProcTerm()
	if term == nil {
		t.Fatal("NewProcTerm returned a nil git-ticket Terminal")
	}
}

// TestHandoffTermSatisfiesGitTicketTerminal gives the wrapper's
// assertion the same named home, and checks that wrapping a real
// ProcTerm still produces something view.Run accepts.
func TestHandoffTermSatisfiesGitTicketTerminal(t *testing.T) {
	var term gttui.Terminal = NewHandoffTerm(NewProcTerm())
	if term == nil {
		t.Fatal("NewHandoffTerm returned a nil git-ticket Terminal")
	}
}
