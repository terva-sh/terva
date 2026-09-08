package modes

// Lending stdin to a guest full-screen application.
//
// terva's input goroutine owns stdin for the life of the session. By the
// time any handoff starts it is already blocked inside Terminal.ReadByte,
// which means it has already claimed the next byte the user types. A
// guest that called ReadByte itself would race it for every keystroke,
// and each byte would land with whichever reader happened to wake first.
// Half the guest's keys would go missing, and terva would act on the
// other half behind the guest's screen.
//
// So the guest never touches the terminal. The one reader keeps reading,
// and while a route is installed it forwards each byte here instead of
// decoding it. terva's key decoder stays parked inside a single ReadByte
// call for the whole visit, which is exactly the pause the handoff wants,
// and no byte is dropped at either edge: the byte that arrives as the
// route goes up is forwarded, and the first byte after it comes down is
// decoded normally.

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// handoffRoute carries stdin from terva's reader to a guest for the
// length of one visit. It is created per visit and closed once, so a
// stale route can never accept a byte.
type handoffRoute struct {
	bytes chan byte
	done  chan struct{}
	once  sync.Once
}

func newHandoffRoute() *handoffRoute {
	return &handoffRoute{
		// Unbuffered on purpose. The reader should hand a byte straight
		// to the guest and block until the guest takes it, so a guest
		// that stops reading exerts back pressure rather than letting
		// keystrokes pile up in a queue it will never drain.
		bytes: make(chan byte),
		done:  make(chan struct{}),
	}
}

// feed hands one byte to the guest, and gives up when the route closes.
// Without that second case the input goroutine would block forever on a
// guest that has already left, which would wedge every later keystroke
// in the session.
func (r *handoffRoute) feed(b byte) {
	select {
	case r.bytes <- b:
	case <-r.done:
	}
}

// close ends the route. It is safe to call more than once, so the
// handoff can defer it and still close early on an error path.
func (r *handoffRoute) close() {
	r.once.Do(func() { close(r.done) })
}

// ReadByte blocks until terva's reader forwards a byte. A closed route
// reports io.EOF, which git-ticket's view.Run treats as a quit: the
// terminal is gone, so there is nobody left to report to.
func (r *handoffRoute) ReadByte() (byte, error) {
	select {
	case b := <-r.bytes:
		return b, nil
	case <-r.done:
		return 0, io.EOF
	}
}

// PeekByteTimeout answers (0, false, nil) when no byte arrives within d.
// The guest uses it to tell a bare Esc from the start of an escape
// sequence, so "nothing arrived" has to be an ordinary answer and not an
// error.
func (r *handoffRoute) PeekByteTimeout(d time.Duration) (byte, bool, error) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case b := <-r.bytes:
		return b, true, nil
	case <-t.C:
		return 0, false, nil
	case <-r.done:
		return 0, false, nil
	}
}

// readByteRouted is the input goroutine's byte source, and the only
// caller of Terminal.ReadByte in a session.
//
// With no route installed it is the terminal's own ReadByte. With one
// installed it forwards bytes to the guest and keeps reading, so this
// call does not return until the guest is gone. That parks terva's key
// decoder for the length of the visit without stopping the goroutine
// that owns the file descriptor.
func (i *Interactive) readByteRouted() (byte, error) {
	for {
		b, err := i.cfg.Terminal.ReadByte()
		if err != nil {
			// stdin is gone. Close the route so the guest quits too,
			// rather than waiting on a reader that will never read again.
			if r := i.handoff.Load(); r != nil {
				r.close()
			}
			return 0, err
		}
		r := i.handoff.Load()
		if r == nil {
			return b, nil
		}
		r.feed(b)
	}
}

// peekByteRouted is the input goroutine's peek. While a guest holds
// stdin it reports that no byte is available rather than reading one.
//
// The decoder is normally parked inside readByteRouted while a route is
// up and never reaches this. The exception is a user who typed ahead: the
// decoder can be part way through an escape sequence when the route goes
// up, and a peek straight to the terminal would take a byte meant for the
// guest.
func (i *Interactive) peekByteRouted(d time.Duration) (byte, bool, error) {
	if r := i.handoff.Load(); r != nil {
		return 0, false, nil
	}
	return i.cfg.Terminal.PeekByteTimeout(d)
}

// handoffTerminal is the terminal a guest sees: terva's own, wrapped so
// that the guest cannot restore the host's raw mode or leave resize
// callbacks behind, with stdin replaced by the route.
//
// tui.HandoffTerm owns the first two differences and knows nothing about
// terva. The stdin swap is the third, and it belongs here because the
// thing being worked around is terva's own input goroutine.
type handoffTerminal struct {
	*tui.HandoffTerm
	route *handoffRoute
}

func (t *handoffTerminal) ReadByte() (byte, error) { return t.route.ReadByte() }

func (t *handoffTerminal) PeekByteTimeout(d time.Duration) (byte, bool, error) {
	return t.route.PeekByteTimeout(d)
}

// newHandoffTerminal builds the guest's terminal over the host's.
func newHandoffTerminal(host tui.Terminal, route *handoffRoute) *handoffTerminal {
	return &handoffTerminal{HandoffTerm: tui.NewHandoffTerm(host), route: route}
}

// ticketStoreAvailable reports whether a ledger governs this session's
// directory. It is probed rather than cached: ticket_init can create a
// store mid-session, and a probe is a few stat calls on a path a person
// only reaches by typing a slash.
//
// The --no-ticket opt-out is deliberately not consulted. That flag
// withdraws the ticket_* tools from the MODEL, and its documented
// contract is that the ledger stays reachable to the person: `terva
// ticket` survives it, and so does this.
func (i *Interactive) ticketStoreAvailable() bool {
	return tools.TicketStoreAvailable(i.cfg.CWD)
}

// slashTicket hands the terminal to git-ticket's own UI, and takes it
// back when the user quits.
//
// It runs on the main loop and blocks it for the whole visit. That is
// the point rather than a cost: the main loop is the only goroutine that
// paints, so parking it is what stops terva drawing over a screen it no
// longer owns. A resize arriving meanwhile lands in the buffered resize
// channel and repaints once, on return.
func (i *Interactive) slashTicket(_ context.Context, _ []string, _ string) bool {
	if !i.ticketStoreAvailable() {
		i.setStatusErr(i18n.T("no ticket store here - run `terva ticket init` to make one"))
		return false
	}

	route := newHandoffRoute()
	// The swap is what makes a second /ticket impossible, and it has to be
	// atomic: the attach path can dispatch a slash command off the main
	// loop, so two handoffs could otherwise install two routes and split
	// the user's keystrokes between them.
	if !i.handoff.CompareAndSwap(nil, route) {
		i.setStatusErr(i18n.T("the ticket ledger is already open"))
		return false
	}
	defer i.handoff.Store(nil)
	defer route.close()

	// Cancelling the slot closes the route, so an esc or a cancel from
	// another surface ends the visit instead of leaving the guest holding
	// a terminal nobody is feeding.
	if !i.turns.claimSlot(route.close) {
		i.setStatusErr(i18n.T("busy - wait for the current turn to finish before opening the ticket ledger"))
		return false
	}
	defer i.turns.releaseSlot()

	err := i.runTicketHandoff(route)
	if err != nil {
		i.setStatusErr(i18n.T("ticket ledger: %s", oneLine(err.Error())))
		return false
	}
	i.setStatusOK(i18n.T("closed the ticket ledger"))
	return false
}

// oneLine folds a multi-line message into a single row.
//
// The status line is one string rendered as one row, so a newline in it
// puts the following text on its own alignment and the message scatters
// across the screen. The guest's errors are git-ticket's, not ours, so
// this guards the whole class rather than the one message that shipped
// broken.
func oneLine(s string) string {
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

// runTicketHandoff is the visit itself: disarm, hand over, take back.
//
// The screen needs no work from us in either direction. terva stays on
// the terminal's main screen and git-ticket's Frame.Start switches to the
// alternate one, so the two never contend for a buffer and Frame.Stop
// puts terva's screen and scrollback back untouched.
func (i *Interactive) runTicketHandoff(route *handoffRoute) error {
	term := newHandoffTerminal(i.cfg.Terminal, route)
	// Release detaches the resize callbacks the guest registered. Deferred
	// so it runs on the error paths too, where the guest may have started
	// and registered before failing.
	defer term.Release()

	// Bracketed paste and the enhanced-keyboard protocol are terva's, and
	// git-ticket's reader parses neither: left on, a paste or a modified
	// keypress arrives in the guest as escape-sequence noise.
	// armTerminalModes on the way back turns both on again, and it is the
	// exact inverse of this pair.
	_, _ = i.cfg.Terminal.Write([]byte(tui.SeqEnhancedKeyboardOff + tui.SeqBracketedPasteOff))

	err := tools.RunTicketUIOn(i.cfg.CWD, term)

	i.armTerminalModes()
	// The guest owned the screen and the window may have changed size
	// while it did, so the size is re-read rather than assumed, and the
	// renderer is told to draw a full frame rather than a diff against
	// whatever it painted last.
	if i.rend != nil {
		cols, rows := i.cfg.Terminal.Size()
		i.rend.Resize(cols, rows)
	}
	i.invalidate()
	return err
}
