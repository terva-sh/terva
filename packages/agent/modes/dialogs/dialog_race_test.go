package dialogs

// Both panels that report the outcome of a BACKGROUND action, under the race
// detector.
//
// The /shared and /workflows panels are the two dialogs a host writes to from a
// goroutine: the overlay dispatches `go i.saveSharedFile(…)`, `go
// i.refreshWorkflowRuns()` and `go i.fetchWorkflowRun(…)` so the panel stays
// painted while the wire call runs, and each of those reports back with
// Notice() or SetError(). The render goroutine reads the very same fields on
// every frame. Nothing in either dialog synchronized that, and no test ran the
// two goroutines together — so `go test -race` had never once looked at the
// pair.
//
// These tests are the ones that look. Every case here fails under -race against
// an unguarded dialog, and the deadlock case fails against a guard that is too
// greedy, which is the other way to get this wrong.

import (
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/tui"
)

// raceRounds is enough interleaving for the detector to see the pair without
// making the suite slow. The detector reports on the FIRST unsynchronized
// access it observes, so this does not need to be large.
const raceRounds = 200

// TestSharedDialogNoticeRacesRender is the exact shape the overlay creates:
// `go i.saveSharedFile(id)` ends in sharedDialog.Notice(...) while the render
// goroutine paints the panel.
func TestSharedDialogNoticeRacesRender(t *testing.T) {
	d := NewSharedDialog()
	rows := sharedFixture()
	d.Open(func() []ctrlproto.SharedFileEntry { return rows })

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // the host's background save reporting its outcome
		defer wg.Done()
		for i := range raceRounds {
			if i%2 == 0 {
				d.Notice("saved chart.png", false)
			} else {
				d.Notice("save failed: swept", true)
			}
		}
	}()
	go func() { // the render goroutine
		defer wg.Done()
		for range raceRounds {
			_ = d.Render(tui.Theme{}, 80)
		}
	}()
	wg.Wait()
}

// TestWorkflowDialogSetErrorRacesRender is the same shape one panel over:
// `go i.refreshWorkflowRuns()` and `go i.fetchWorkflowRun(id)` both report
// through SetError, which writes err AND loading.
func TestWorkflowDialogSetErrorRacesRender(t *testing.T) {
	d := NewWorkflowDialog()
	d.MaxRows = 40
	d.Open(
		func() []ctrlproto.WorkflowRunInfo { return wfRuns },
		func() *ctrlproto.WorkflowRunView { return nil },
	)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range raceRounds {
			if i%2 == 0 {
				d.SetError("the daemon refused")
			} else {
				d.SetError("")
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range raceRounds {
			_ = d.Render(tui.Theme{}, 100)
		}
	}()
	wg.Wait()
}

// TestWorkflowDialogOpenedRacesHandleKey covers the crossing that runs the
// other way.
//
// fetchWorkflowRun reads Opened() from its goroutine to decide whether the
// answer still belongs on screen — the guard that stops a slow read painting
// over whatever the operator moved to. Meanwhile the main goroutine's keys
// write `open`: Enter opens a run, Esc backs out. The staleness check and the
// thing it checks against were never synchronized.
func TestWorkflowDialogOpenedRacesHandleKey(t *testing.T) {
	d := NewWorkflowDialog()
	d.MaxRows = 40
	d.Open(
		func() []ctrlproto.WorkflowRunInfo { return wfRuns },
		func() *ctrlproto.WorkflowRunView { return nil },
	)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // the in-flight fetch asking "is this still the open run?"
		defer wg.Done()
		for range raceRounds {
			_ = d.Opened()
		}
	}()
	go func() { // the operator opening a run and backing out of it
		defer wg.Done()
		for range raceRounds {
			d.HandleKey(tui.Key{Kind: tui.KeyEnter})
			d.HandleKey(tui.Key{Kind: tui.KeyEsc})
		}
	}()
	wg.Wait()
}

// TestDialogLocksAreNotHeldAcrossHostCallbacks is the other failure mode, and
// the reason this fix is not "put a mutex around every method".
//
// The dialogs hold no data: listFn and viewFn call back into the host, which
// takes the host's own lock to read its caches. The host meanwhile calls INTO
// the dialog while holding that lock — workflowRunView() takes i.mu and then
// asks Opened() which run is on screen. So the two locks are acquired in both
// orders, and a dialog lock held across a callback closes the cycle: render
// holds the dialog and waits for the host, the host holds itself and waits for
// the dialog, and the TUI is wedged with no error and no panic.
//
// Deadlock does not fail a test, it hangs it, so this runs the pair against a
// deadline and reports rather than waiting for the suite's own timeout.
func TestDialogLocksAreNotHeldAcrossHostCallbacks(t *testing.T) {
	var host sync.Mutex // stands in for Interactive.mu

	shared := NewSharedDialog()
	shared.Open(func() []ctrlproto.SharedFileEntry {
		// sharedFileRows does exactly this: take the host lock, copy, release.
		host.Lock()
		defer host.Unlock()
		return sharedFixture()
	})

	wf := NewWorkflowDialog()
	wf.MaxRows = 40
	wf.Open(
		func() []ctrlproto.WorkflowRunInfo {
			host.Lock()
			defer host.Unlock()
			return wfRuns
		},
		func() *ctrlproto.WorkflowRunView {
			host.Lock()
			defer host.Unlock()
			return nil
		},
	)

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		wg.Add(4)
		// Two render goroutines: dialog lock first, then the host's.
		go func() {
			defer wg.Done()
			for range raceRounds {
				_ = shared.Render(tui.Theme{}, 80)
			}
		}()
		go func() {
			defer wg.Done()
			for range raceRounds {
				_ = wf.Render(tui.Theme{}, 100)
			}
		}()
		// Two host goroutines: host lock first, then the dialog's — the
		// opposite order, which is what makes this a cycle rather than a queue.
		go func() {
			defer wg.Done()
			for range raceRounds {
				host.Lock()
				_ = wf.Opened()
				host.Unlock()
			}
		}()
		go func() {
			defer wg.Done()
			for range raceRounds {
				host.Lock()
				shared.Notice("saved", false)
				host.Unlock()
			}
		}()
		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("deadlock: a dialog lock is held across a host callback — " +
			"render takes the dialog then the host, the host takes itself then the dialog")
	}
}
