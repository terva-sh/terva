// Package widgets holds the TUI components that both the interactive
// loop and the modal dialogs draw with: the status spinner, the
// @-triggered file suggester, path tab-completion for an editor buffer,
// and ANSI stripping.
//
// It exists to be the bottom of packages/agent/modes. The loop and the
// dialogs each own one of these components, so a package boundary drawn
// between those two would put this code on both sides of it. Pulling the
// shared leaves down first turns that boundary into a plain git mv.
//
// The rule for this package: it imports tui, ignore and the standard
// library, and nothing from packages/agent. It knows how to draw a
// thing; it does not know what an Interactive, a Carrier or an Agent is.
// If a widget starts needing one, it is not a widget.
package widgets
