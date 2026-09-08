package tui

// ProcTerm's resize registry: one handler for the process, one
// invocation per callback per resize, and a detach that works.
//
// The bug these hold shut (TKT-01M1Z5HX3P): OnResize used to append to a
// slice and call installResizeHandler on EVERY call. That left a
// goroutine and a SIGWINCH subscription per registration, and each
// goroutine walked the whole callback slice, so N callbacks meant N
// squared invocations for one resize. There was no way to remove a
// callback, and the slice was appended to without a lock while the
// signal goroutines ranged over it.
//
// Nothing was broken in production because interactive.go was the only
// caller and registered once. N=1 hides every one of these.

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// One resize invokes each registered callback exactly once. Under the
// old fan-out this reported three invocations per callback, because
// three registrations meant three goroutines each walking three
// callbacks.
func TestFireResizeInvokesEachCallbackOnce(t *testing.T) {
	p := NewProcTerm()
	var a, b, c atomic.Int32
	p.OnResize(func() { a.Add(1) })
	p.OnResize(func() { b.Add(1) })
	p.OnResize(func() { c.Add(1) })

	p.fireResize()

	for name, got := range map[string]int32{"a": a.Load(), "b": b.Load(), "c": c.Load()} {
		if got != 1 {
			t.Errorf("callback %s fired %d times, want 1", name, got)
		}
	}
}

// A detached callback stops firing and the others keep firing.
func TestOnResizeDetachStopsOneCallback(t *testing.T) {
	p := NewProcTerm()
	var kept, dropped atomic.Int32
	p.OnResize(func() { kept.Add(1) })
	detach := p.OnResizeDetach(func() { dropped.Add(1) })

	p.fireResize()
	if kept.Load() != 1 || dropped.Load() != 1 {
		t.Fatalf("before detach: kept=%d dropped=%d, want 1 and 1", kept.Load(), dropped.Load())
	}

	detach()
	p.fireResize()

	if kept.Load() != 2 {
		t.Errorf("kept callback fired %d times, want 2", kept.Load())
	}
	if dropped.Load() != 1 {
		t.Errorf("detached callback fired %d times, want 1 (it should not fire after detach)", dropped.Load())
	}
}

// Detaching twice is safe. A caller that defers its detach and also
// calls it on an early return must not panic or remove somebody else's
// callback, which is what a naive index-based removal would do.
func TestOnResizeDetachTwiceIsSafe(t *testing.T) {
	p := NewProcTerm()
	var first, second atomic.Int32
	detach := p.OnResizeDetach(func() { first.Add(1) })
	p.OnResize(func() { second.Add(1) })

	detach()
	detach()

	p.fireResize()

	if first.Load() != 0 {
		t.Errorf("detached callback fired %d times, want 0", first.Load())
	}
	if second.Load() != 1 {
		t.Errorf("surviving callback fired %d times, want 1; a second detach removed the wrong entry", second.Load())
	}
}

// A nil callback is refused rather than stored, and the returned detach
// is still callable so a caller need not special-case it.
func TestOnResizeDetachNilCallback(t *testing.T) {
	p := NewProcTerm()
	detach := p.OnResizeDetach(nil)
	detach()
	p.fireResize() // must not panic on a nil entry
}

// Registering many callbacks adds one goroutine, not one per call.
//
// The bound is deliberately loose. This asserts the shape of the fix
// rather than an exact count, because the test binary has goroutines of
// its own that start and stop. The discriminator is wide enough to be
// safe: the old code added 20 here and the fixed code adds 1.
func TestOnResizeInstallsOneHandlerGoroutine(t *testing.T) {
	p := NewProcTerm()

	// Register once first, so the single handler goroutine is already
	// running and does not count against the delta below.
	p.OnResize(func() {})
	settleGoroutines()
	before := runtime.NumGoroutine()

	for i := 0; i < 20; i++ {
		p.OnResize(func() {})
	}
	settleGoroutines()
	after := runtime.NumGoroutine()

	if delta := after - before; delta > 5 {
		t.Errorf("20 registrations added %d goroutines, want at most 5; installResizeHandler is running per call again", delta)
	}
}

// settleGoroutines gives a just-started goroutine time to appear in the
// count and a just-finished one time to leave it.
func settleGoroutines() {
	for i := 0; i < 10; i++ {
		runtime.Gosched()
		time.Sleep(2 * time.Millisecond)
	}
}

// Registration, detach, and fan-out run concurrently without a data
// race. Run under -race, this is the test that would have caught the
// unsynchronised append: the signal goroutine ranged over resizeCBs
// while the main loop appended to it.
func TestOnResizeConcurrentRegistrationAndFire(t *testing.T) {
	p := NewProcTerm()
	var hits atomic.Int32
	var wg sync.WaitGroup

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			detach := p.OnResizeDetach(func() { hits.Add(1) })
			p.fireResize()
			detach()
		}()
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.fireResize()
		}()
	}
	wg.Wait()

	// The count is timing-dependent by construction, so assert only
	// that the fan-out ran and nothing deadlocked.
	if hits.Load() == 0 {
		t.Error("no callback fired across 16 concurrent goroutines")
	}
}

// A callback that registers another one during the fan-out must not
// deadlock. fireResize snapshots under the lock and invokes outside it
// precisely so this is safe.
func TestFireResizeCallbackMayRegister(t *testing.T) {
	p := NewProcTerm()
	done := make(chan struct{})

	p.OnResize(func() {
		p.OnResize(func() {})
		close(done)
	})

	go p.fireResize()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("fireResize deadlocked when a callback registered another callback")
	}
}
