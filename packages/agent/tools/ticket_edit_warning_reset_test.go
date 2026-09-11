package tools

import (
	"sync"
	"testing"
)

// The direct-edit warning fires once per TURN, not once per session. Reset is
// the turn boundary, called by build.TicketEditWarnResetObserver on every
// core.EvTurnStart.
//
// The value of the whole mechanism is that a model working autonomously for
// thirty steps is reminded more than once. These tests hold the warner's half:
// that a reset re-arms it, that a reset cannot switch it on, and that a reset
// arriving on the event goroutine is safe against tool calls in flight.

const ticketFile = ".tickets/tickets/TKT-01M242D2VB60JWGR6ETSY9QM01.md"

func enabledWarner() *TicketEditWarner {
	w := &TicketEditWarner{}
	w.Enable()
	return w
}

func TestTicketEditWarningReturnsAfterAReset(t *testing.T) {
	w := enabledWarner()

	if got := w.Notice(ticketFile); got == "" {
		t.Fatal("the first ticket write in a turn must warn")
	}
	if got := w.Notice(ticketFile); got != "" {
		t.Errorf("a second write in the SAME turn must stay quiet, got %q.\n"+
			"However many ticket files one step touches, it is one warning.", got)
	}

	w.Reset()

	if got := w.Notice(ticketFile); got == "" {
		t.Error("after a turn boundary the warning must return.\n" +
			"Without this the mechanism is the once-per-session behaviour it replaced, " +
			"and a model working for thirty steps is reminded once.")
	}
	if got := w.Notice(ticketFile); got != "" {
		t.Errorf("the new turn must also warn only once, got %q", got)
	}
}

// Several turns in a row, because a reset that worked once and then latched
// would still pass a single round trip.
func TestTicketEditWarningReturnsEveryTurn(t *testing.T) {
	w := enabledWarner()

	for turn := 1; turn <= 5; turn++ {
		if got := w.Notice(ticketFile); got == "" {
			t.Fatalf("turn %d: the first write must warn", turn)
		}
		for extra := 0; extra < 3; extra++ {
			if got := w.Notice(ticketFile); got != "" {
				t.Fatalf("turn %d: write %d must stay quiet, got %q", turn, extra+2, got)
			}
		}
		w.Reset()
	}
}

// Reset must not be able to switch the warning ON. Enable is decided by the
// built registry, so a session whose ticket tools were pruned never earned the
// warning and must not start getting it at the first turn boundary.
func TestTicketEditWarningResetCannotEnable(t *testing.T) {
	w := &TicketEditWarner{} // never enabled

	w.Reset()
	if got := w.Notice(ticketFile); got != "" {
		t.Errorf("a warner nobody enabled must stay silent through a reset, got %q.\n"+
			"Enable reflects whether the ticket write tools survived pruning; a turn "+
			"boundary knows nothing about that and must not overrule it.", got)
	}

	for i := 0; i < 3; i++ {
		w.Reset()
		if got := w.Notice(ticketFile); got != "" {
			t.Fatalf("still silent after %d resets, got %q", i+1, got)
		}
	}
}

func TestTicketEditWarningResetIsSafeOnNil(t *testing.T) {
	var w *TicketEditWarner
	w.Reset() // a tool with no warner is a supported state; this must not panic
}

// Criterion 4. Reset arrives on the serial event goroutine while tool calls run
// in parallel, so the two genuinely meet. This is the reason the warner carries
// a mutex rather than a plain bool, and -race is what makes the test mean
// something.
func TestTicketEditWarningResetRacesToolCalls(t *testing.T) {
	w := enabledWarner()

	// Two groups on purpose. The resetter runs until it is told to stop, so it
	// must NOT sit in the group whose completion does the telling: waiting on a
	// group that contains the goroutine waiting for that wait is a deadlock.
	var workers, resetter sync.WaitGroup
	stop := make(chan struct{})

	// The event goroutine: turn boundaries, one after another.
	resetter.Add(1)
	go func() {
		defer resetter.Done()
		for {
			select {
			case <-stop:
				return
			default:
				w.Reset()
			}
		}
	}()

	// Tool calls: several in one step, as parallel execution really does.
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for n := 0; n < 500; n++ {
				w.Notice(ticketFile)
			}
		}()
	}

	// Enable races too: a rebuild can bind the warning while a turn runs.
	workers.Add(1)
	go func() {
		defer workers.Done()
		for n := 0; n < 500; n++ {
			w.Enable()
		}
	}()

	workers.Wait()
	close(stop)
	resetter.Wait()
}
