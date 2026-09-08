package tui

import "sync"

// HandoffTerm lends terva's terminal to a guest full-screen application
// for as long as that guest is on screen, then takes it back whole.
//
// The guest here is git-ticket's TUI, reached through view.Run. Two of
// the seven Terminal methods mean something different to a guest than
// they do to the host, and this wrapper is those two methods. Every
// other method passes through to the wrapped terminal untouched.
//
// The wrapper names no guest and imports nothing from one. terva's
// Terminal and git-ticket's tui.Terminal declare identical methods, so
// a *HandoffTerm satisfies both. packages/tui/terminal_gitticket_test.go
// holds that as a compile-time assertion.
//
// The guest is not a subprocess. It runs in this process, on this
// goroutine, writing to the same file descriptors. Nothing stops it
// from leaving state behind, so the wrapper is what makes the loan
// reversible.
type HandoffTerm struct {
	// Terminal is the host terminal, embedded so that Write, Size,
	// ReadByte, PeekByteTimeout, and SetNonblock reach it directly. A
	// method added to the interface later passes through the same way,
	// which is the right default: a method needs wrapping only when the
	// guest's idea of it differs from the host's.
	Terminal

	mu       sync.Mutex
	detaches []func()
}

// NewHandoffTerm wraps inner for the length of one loan. Release ends
// the loan. The wrapper holds no state that a second loan would inherit,
// so a caller may make a new one per handoff or reuse this one.
func NewHandoffTerm(inner Terminal) *HandoffTerm {
	return &HandoffTerm{Terminal: inner}
}

// EnterRaw reports success and returns a restore that does nothing.
//
// terva puts the tty into raw mode once, in the interactive run loop,
// and holds it for the whole session. A guest cannot know that. It
// calls EnterRaw on the way in and calls the restore it got on the way
// out, which is correct of it: a guest that owns the terminal must hand
// back the mode it found.
//
// Here it did not find cooked mode, it found raw. Running the real
// restore would return the tty to cooked while terva is still reading
// keys from it, and every keystroke after the guest exits would arrive
// line-buffered. The guest has no change of its own to undo, so a
// restore that does nothing is the honest answer rather than a dodge.
//
// The error is always nil. Raw mode is already held, so there is no
// failure left to report.
func (h *HandoffTerm) EnterRaw() (func() error, error) {
	return func() error { return nil }, nil
}

// OnResize registers a resize callback that Release takes back.
//
// ProcTerm.OnResize keeps a callback for the life of the process. That
// is right for terva's own repaint, which lives as long as the session,
// and wrong for a guest, which lives for one visit. git-ticket's
// Frame.Start and view.Run each register one, so without this every
// handoff would leave two more callbacks behind. After ten visits a
// single SIGWINCH would run twenty stale repaints, all of them drawing
// a screen their owner has already left.
//
// Registration goes through OnResizeDetach when the wrapped terminal
// offers it, which *ProcTerm and tuitest.FakeTerm both do. A terminal
// without it still gets the callback, and that callback still stays for
// the life of the process. That is the behavior such a terminal has
// today, so the wrapper makes it no worse.
//
// A nil callback is dropped rather than recorded. Firing it would panic
// on a resize, far from whoever passed it.
func (h *HandoffTerm) OnResize(fn func()) {
	if fn == nil {
		return
	}
	detach := attachResize(h.Terminal, fn)
	h.mu.Lock()
	h.detaches = append(h.detaches, detach)
	h.mu.Unlock()
}

// Release detaches every callback the guest registered through this
// wrapper. Call it once the guest is off screen, before the host
// repaints.
//
// It is safe to call more than once, and safe to call when the guest
// registered nothing. The wrapper stays usable afterwards: a callback
// registered after a Release is detached by the next one, so a caller
// that reuses one wrapper across visits does not accumulate anything.
//
// This does not touch the host's own callbacks. They were registered on
// the terminal directly, not through the wrapper, and the host still
// wants them.
func (h *HandoffTerm) Release() {
	h.mu.Lock()
	detaches := h.detaches
	h.detaches = nil
	h.mu.Unlock()

	// Called outside the lock. A detach that registers or releases
	// again would otherwise deadlock, and ProcTerm.fireResize takes the
	// same care for the same reason.
	for _, detach := range detaches {
		detach()
	}
}

// attachResize registers fn and returns a function that removes it.
//
// The detach upgrade is deliberately absent from the Terminal interface,
// because that interface has to keep matching git-ticket's method for
// method. A type assertion is how terva reaches every optional terminal
// capability, and OnResizeDetach documents this as its intended use.
func attachResize(t Terminal, fn func()) func() {
	if d, ok := t.(interface{ OnResizeDetach(func()) func() }); ok {
		return d.OnResizeDetach(fn)
	}
	t.OnResize(fn)
	return func() {}
}
