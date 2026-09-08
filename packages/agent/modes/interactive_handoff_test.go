package modes

// The stdin loan behind /ticket, and the gate that decides where the
// command is offered.

import (
	"strings"
	"testing"
	"time"

	gttui "github.com/terva-sh/git-ticket/tui"

	"terva.sh/terva/packages/tui/tuitest"
)

// The status line is one string rendered as one row. A guest error with
// newlines put each line on its own alignment and scattered a YAML block
// across the screen, which shipped. The errors here are git-ticket's, so
// the guard covers the class rather than the one message that broke.
func TestOneLineFoldsAMultiLineGuestError(t *testing.T) {
	in := "this store records no actor.\nReplace `actors: []` in /p/config.yml:\n\nactors:\n  - id: human:you\n    name: Your Name\n"

	got := oneLine(in)

	if strings.Contains(got, "\n") {
		t.Errorf("oneLine left a newline: %q", got)
	}
	if strings.Contains(got, "  ") {
		t.Errorf("oneLine left a run of spaces, which a status line renders as a gap: %q", got)
	}
	// Folding must not lose the content, only the layout.
	for _, want := range []string{"records no actor", "- id: human:you", "name: Your Name"} {
		if !strings.Contains(got, want) {
			t.Errorf("oneLine dropped %q from the message: %q", want, got)
		}
	}
}

// The guest's terminal is handed to git-ticket's view.Run, so it has to
// satisfy git-ticket's Terminal. It overrides ReadByte and
// PeekByteTimeout over an embedded *tui.HandoffTerm, and this catches an
// override whose signature drifts from the method it means to replace.
var _ gttui.Terminal = (*handoffTerminal)(nil)

// readRouted reads one byte from the guest side, failing the test rather
// than hanging the suite when nothing arrives.
func readRouted(t *testing.T, r *handoffRoute) byte {
	t.Helper()
	type result struct {
		b   byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		b, err := r.ReadByte()
		ch <- result{b, err}
	}()
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatalf("guest ReadByte: %v", got.err)
		}
		return got.b
	case <-time.After(2 * time.Second):
		t.Fatal("no byte reached the guest; the reader is not forwarding")
		return 0
	}
}

// The whole point of the route: terva's reader keeps owning stdin, and
// the guest gets the bytes. While the loan is up the decoder must see
// none of them, or terva would act on keystrokes behind the guest's
// screen.
func TestReadByteRoutedForwardsToTheGuest(t *testing.T) {
	fake := tuitest.NewFakeTerm(80, 24)
	i := &Interactive{cfg: InteractiveConfig{Terminal: fake}}

	route := newHandoffRoute()
	i.handoff.Store(route)

	// The decoder side, which is what the input goroutine runs.
	decoded := make(chan byte, 8)
	go func() {
		for {
			b, err := i.readByteRouted()
			if err != nil {
				return
			}
			decoded <- b
		}
	}()

	fake.Type("ab")

	for _, want := range []byte{'a', 'b'} {
		if got := readRouted(t, route); got != want {
			t.Fatalf("guest got %q, want %q", got, want)
		}
	}

	select {
	case b := <-decoded:
		t.Fatalf("the decoder received %q while a guest held stdin", b)
	case <-time.After(50 * time.Millisecond):
	}

	// End the loan. The next byte belongs to terva again, and nothing
	// typed during the visit is replayed into the transcript.
	i.handoff.Store(nil)
	route.close()

	fake.Type("c")
	select {
	case b := <-decoded:
		if b != 'c' {
			t.Fatalf("the decoder got %q after the handoff, want %q", b, 'c')
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the decoder never got a byte after the handoff ended; the reader stayed parked")
	}
}

// A guest that has already left must not wedge the reader. Without the
// done case in feed, this byte would block the input goroutine forever
// and every later keystroke in the session with it.
func TestHandoffRouteFeedGivesUpAfterClose(t *testing.T) {
	route := newHandoffRoute()
	route.close()

	done := make(chan struct{})
	go func() {
		route.feed('x')
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("feed blocked on a closed route; the input goroutine would never read again")
	}
}

// A closed route reports EOF, which git-ticket's reader treats as a
// quit. That is how a lost terminal ends the guest instead of leaving it
// waiting on a reader that will never read again.
func TestHandoffRouteReadByteReportsEOFAfterClose(t *testing.T) {
	route := newHandoffRoute()
	route.close()

	if _, err := route.ReadByte(); err == nil {
		t.Fatal("ReadByte on a closed route returned no error; the guest would never quit")
	}
}

// Peek has to answer "nothing arrived" as an ordinary result, because
// the guest uses it to tell a bare Esc from the start of an escape
// sequence. An error there would read as a broken terminal.
func TestHandoffRoutePeekTimesOutWithoutError(t *testing.T) {
	route := newHandoffRoute()

	b, ok, err := route.PeekByteTimeout(10 * time.Millisecond)
	if err != nil {
		t.Fatalf("peek with no byte available returned an error: %v", err)
	}
	if ok {
		t.Fatalf("peek reported a byte %q that nobody sent", b)
	}
}

// While a guest holds stdin, terva's decoder must not peek a byte
// straight from the terminal. It reaches here only when the user typed
// ahead and the decoder was part way through an escape sequence as the
// loan went up.
func TestPeekByteRoutedDoesNotStealFromTheGuest(t *testing.T) {
	fake := tuitest.NewFakeTerm(80, 24)
	i := &Interactive{cfg: InteractiveConfig{Terminal: fake}}
	fake.Type("z")

	route := newHandoffRoute()
	i.handoff.Store(route)

	b, ok, err := i.peekByteRouted(50 * time.Millisecond)
	if err != nil {
		t.Fatalf("peek during a handoff: %v", err)
	}
	if ok {
		t.Fatalf("peek took %q from the terminal while a guest held stdin", b)
	}

	// The byte is still there for the guest.
	i.handoff.Store(nil)
	route.close()
	if got, err := fake.ReadByte(); err != nil || got != 'z' {
		t.Fatalf("terminal byte after the peek = (%q, %v), want ('z', nil)", got, err)
	}
}

// /ticket opens a repository's ticket ledger, so it is offered only
// where there is one. The catalog feeds both the autocomplete popup and
// /help, so this is the single gate behind both.
func TestTicketCommandOfferedOnlyWithAStore(t *testing.T) {
	withStore := builtinSlashCatalog(true)
	if !catalogHas(withStore, "/ticket") {
		t.Error("/ticket is missing from the catalog in a repository that has a ticket store")
	}

	withoutStore := builtinSlashCatalog(false)
	if catalogHas(withoutStore, "/ticket") {
		t.Error("/ticket is offered in a repository with no ticket store")
	}

	// Nothing else may vanish with it. A filter that took the rest of
	// the group with it would pass the check above.
	for _, name := range []string{"/help", "/mcp", "/swarm", "/extensions"} {
		if !catalogHas(withoutStore, name) {
			t.Errorf("%s disappeared along with /ticket", name)
		}
	}
	if len(withStore)-len(withoutStore) != 1 {
		t.Errorf("the store gate changed the catalog by %d entries, want exactly 1", len(withStore)-len(withoutStore))
	}
}

// Hidden from the popup is not the same as unknown. An unrecognised head
// is sent to the model as a prompt, and "/ticket" is not a question
// anybody meant to ask, so it stays dispatchable and the handler
// explains itself.
func TestTicketCommandStaysDispatchableWithoutAStore(t *testing.T) {
	if catalogHas(builtinSlashCatalog(false), "/ticket") {
		t.Fatal("precondition: /ticket should be hidden without a store")
	}
	if _, ok := lookupSlash("/ticket"); !ok {
		t.Error("/ticket must stay dispatchable so it is never sent to the model as a prompt")
	}
	if _, ok := lookupSlash("/tickets"); !ok {
		t.Error("the /tickets alias must dispatch too")
	}
	if !isKnownSlashCommand("/ticket") {
		t.Error("/ticket must read as a known command")
	}
}

func catalogHas(cat []slashCommand, name string) bool {
	for _, c := range cat {
		if !c.Header && c.Name == name {
			return true
		}
	}
	return false
}
