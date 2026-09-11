package tools

import (
	"path/filepath"
	"strings"
	"sync"
)

// TicketEditWarner nudges a model that writes the ticket store with `edit` or
// `write` instead of the ticket tools.
//
// A direct write to a ticket file carries no revision precondition and no
// actor, so the store records neither who changed it nor what it was based on.
// A concurrent writer loses silently, and the merge driver gets plain text
// where it expects ticket semantics.
//
// The session behind this warner did that five times. Three of those passed
// replaceAll on the string for an unchecked box, and one such edit checks every
// acceptance criterion and every definition-of-done item in the file at once,
// with no judgement per criterion.
//
// It warns and never refuses. When the ticket tools are absent, from
// --no-ticket or a repository with no store, a direct edit is the only way to
// touch a store at all, and refusing would remove the last route. Enable is
// what gates that, and it is called only when the ticket write tools survived
// pruning.
//
// ONE WARNING PER TURN. Nothing per-turn reaches a tool: the tool context
// carries no step, and the ticket card refreshes on a timer rather than a turn
// boundary. So the boundary arrives from outside, as an event observer that
// calls Reset on every core.EvTurnStart. build.TicketEditWarnResetObserver is
// that observer, and each host composes it.
//
// A host that fails to compose it degrades to one warning per session rather
// than breaking, which is quiet enough to miss. That is why a test holds every
// host site rather than trusting the wiring to stay put.
type TicketEditWarner struct {
	mu      sync.Mutex
	enabled bool
	warned  bool
}

// ticketEditWarningText names the tools that do the job the edit was reaching
// for, because a warning that only scolds leaves the model where it started.
const ticketEditWarningText = "warning: this file is in the ticket store. A direct edit carries no revision precondition and no actor, so the store's audit trail does not hold it. ticket_update changes a field, and ticket_transition moves the status. A task that you close checks its acceptance criterion. This warning appears one time in each turn."

// Enable turns the warning on. The caller decides, because only the built
// registry knows whether the ticket write tools survived --tools and plan mode.
// A warner that is never enabled stays silent for the life of the session.
func (w *TicketEditWarner) Enable() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.enabled = true
}

// Notice returns the warning the first time a turn writes a ticket file, and
// "" every other time in that turn. The lock is real work rather than ceremony:
// tool calls in one step run in parallel, so two edits can reach this together,
// and Reset arrives from the event goroutine while they do.
func (w *TicketEditWarner) Notice(path string) string {
	if w == nil || !isTicketStoreFile(path) {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.enabled || w.warned {
		return ""
	}
	w.warned = true
	return ticketEditWarningText
}

// isTicketStoreFile reports whether path is a ticket, rather than any file that
// happens to sit in the store.
//
// README.md and CONVENTIONS.md are prose a person writes and no ticket tool
// can, so warning on them would be advice with no remedy. config.yml and the
// rest are not Markdown and never match. epics.md DOES match, and that is
// deliberate: ticket_fix rewrites it, so a hand edit there has a tool too.
//
// The segment walk rather than a prefix test on cwd/.tickets: a store can be
// discovered above the working directory, and a worktree reaches its store by a
// path this tool never computed.
func isTicketStoreFile(path string) bool {
	if !strings.EqualFold(filepath.Ext(path), ".md") {
		return false
	}
	switch filepath.Base(path) {
	case "README.md", "CONVENTIONS.md":
		return false
	}
	for _, seg := range strings.Split(filepath.ToSlash(path), "/") {
		if seg == ".tickets" {
			return true
		}
	}
	return false
}

// Reset arms the warning again for a new turn. It leaves enabled alone, because
// what gated the warning is a property of the built registry and not of the
// turn: a session that never earned the warning must not start getting it at
// the first turn boundary.
//
// A tool rebuild mints a fresh warner, so a reset that lost the enabled bit
// would also need the binder to run again to restore it. Only the warned flag
// is disposable state, and losing that to a rebuild costs one extra warning.
func (w *TicketEditWarner) Reset() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.warned = false
}

// TicketEditWarnBinder is how the build wires the warning on without importing
// the concrete tool types. It mirrors TaskBinder: the build walks the registry
// and speaks to whatever implements this.
type TicketEditWarnBinder interface{ EnableTicketEditWarning() }

// TicketEditWarnResetBinder is the same trick for the turn boundary. The
// observer walks the live registry and speaks to whatever implements this, so
// it reaches the warner the CURRENT tools hold. A handle captured once would
// point at a warner a later rebuild replaced, and the reset would then land on
// an instance nobody reads. That failure is silent, so it is worth the walk.
type TicketEditWarnResetBinder interface{ ResetTicketEditWarning() }

// EnableTicketEditWarning satisfies TicketEditWarnBinder.
func (t *EditTool) EnableTicketEditWarning() { t.Tickets.Enable() }

// EnableTicketEditWarning satisfies TicketEditWarnBinder.
func (t *WriteTool) EnableTicketEditWarning() { t.Tickets.Enable() }

// ResetTicketEditWarning satisfies TicketEditWarnResetBinder.
func (t *EditTool) ResetTicketEditWarning() { t.Tickets.Reset() }

// ResetTicketEditWarning satisfies TicketEditWarnResetBinder.
func (t *WriteTool) ResetTicketEditWarning() { t.Tickets.Reset() }
