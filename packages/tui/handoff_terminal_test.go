package tui

// HandoffTerm lends the terminal to a guest and takes it back. The two
// things it changes are the two things a guest gets wrong on a shared
// tty: it restores a raw mode it never established, and it registers
// resize callbacks that outlive it.

import (
	"io"
	"runtime"
	"sync"
	"testing"
	"time"
)

// baseTerm is a Terminal that records what was done to it. It keeps its
// own callback registry rather than wrapping ProcTerm, so a failure
// here names the wrapper and not the terminal underneath.
type baseTerm struct {
	mu       sync.Mutex
	cbs      map[int]func()
	nextID   int
	rawCalls int
	restores int
	written  int
}

func (b *baseTerm) Write(p []byte) (int, error) {
	b.mu.Lock()
	b.written += len(p)
	b.mu.Unlock()
	return len(p), nil
}

func (b *baseTerm) Size() (int, int) { return 80, 24 }

func (b *baseTerm) OnResize(fn func()) { b.register(fn) }

// EnterRaw counts both halves. The restore is the one that matters:
// running it on a shared tty is what drops terva to cooked mode.
func (b *baseTerm) EnterRaw() (func() error, error) {
	b.mu.Lock()
	b.rawCalls++
	b.mu.Unlock()
	return func() error {
		b.mu.Lock()
		b.restores++
		b.mu.Unlock()
		return nil
	}, nil
}

func (b *baseTerm) ReadByte() (byte, error) { return 0, io.EOF }

func (b *baseTerm) PeekByteTimeout(time.Duration) (byte, bool, error) { return 0, false, nil }

func (b *baseTerm) SetNonblock(bool) error { return nil }

func (b *baseTerm) register(fn func()) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cbs == nil {
		b.cbs = make(map[int]func())
	}
	id := b.nextID
	b.nextID++
	b.cbs[id] = fn
	return id
}

func (b *baseTerm) remove(id int) {
	b.mu.Lock()
	delete(b.cbs, id)
	b.mu.Unlock()
}

// fire invokes every registered callback, the way a SIGWINCH would.
func (b *baseTerm) fire() {
	b.mu.Lock()
	cbs := make([]func(), 0, len(b.cbs))
	for _, cb := range b.cbs {
		cbs = append(cbs, cb)
	}
	b.mu.Unlock()
	for _, cb := range cbs {
		cb()
	}
}

func (b *baseTerm) callbacks() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.cbs)
}

func (b *baseTerm) counts() (raw, restores int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rawCalls, b.restores
}

// detachableTerm offers the OnResizeDetach upgrade, as *ProcTerm does.
type detachableTerm struct{ baseTerm }

func (d *detachableTerm) OnResizeDetach(fn func()) func() {
	id := d.register(fn)
	var once sync.Once
	return func() { once.Do(func() { d.remove(id) }) }
}

// plainTerm does not offer the upgrade, which is the fallback path.
type plainTerm struct{ baseTerm }

var (
	_ Terminal = (*detachableTerm)(nil)
	_ Terminal = (*plainTerm)(nil)
)

// The guest calls EnterRaw on the way in and the restore it got on the
// way out. Neither may reach the host terminal: terva already holds raw
// mode, and running the restore returns the tty to cooked while terva is
// still reading keys from it.
func TestHandoffTermEnterRawLeavesTheHostModeAlone(t *testing.T) {
	inner := &detachableTerm{}
	h := NewHandoffTerm(inner)

	restore, err := h.EnterRaw()
	if err != nil {
		t.Fatalf("EnterRaw: %v", err)
	}
	if restore == nil {
		t.Fatal("EnterRaw returned a nil restore, which Frame.Stop would skip silently")
	}
	if raw, _ := inner.counts(); raw != 0 {
		t.Errorf("the guest put the tty into raw mode %d times; the host already holds it", raw)
	}

	if err := restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, restores := inner.counts(); restores != 0 {
		t.Errorf("the guest's restore reached the host terminal %d times; that drops the tty to cooked while terva is still reading keys", restores)
	}
}

// Release takes back what the guest registered and leaves what the host
// registered. git-ticket registers two callbacks per visit, one in
// Frame.Start and one in view.Run.
func TestHandoffTermReleaseTakesBackOnlyGuestCallbacks(t *testing.T) {
	inner := &detachableTerm{}

	// The host registers straight on the terminal, not through the
	// wrapper, exactly as terva's own repaint does.
	hostFires := 0
	inner.OnResize(func() { hostFires++ })

	h := NewHandoffTerm(inner)
	guestFires := 0
	h.OnResize(func() { guestFires++ })
	h.OnResize(func() { guestFires++ })

	inner.fire()
	if guestFires != 2 {
		t.Fatalf("guest callbacks fired %d times on one resize, want 2", guestFires)
	}
	if hostFires != 1 {
		t.Fatalf("host callback fired %d times on one resize, want 1", hostFires)
	}

	h.Release()
	inner.fire()

	if guestFires != 2 {
		t.Errorf("a guest callback fired after Release: it repaints a screen the guest has already left")
	}
	if hostFires != 2 {
		t.Errorf("Release detached the host's own callback, so terva stops repainting on resize")
	}
}

// The acceptance criterion: opening and closing ten times leaves nothing
// behind. A wrapper per visit is the shape the /ticket command will use.
func TestHandoffTermTenVisitsLeaveNothingRegistered(t *testing.T) {
	inner := &detachableTerm{}

	for i := 0; i < 10; i++ {
		h := NewHandoffTerm(inner)
		h.OnResize(func() {})
		h.OnResize(func() {})
		h.Release()
	}

	if n := inner.callbacks(); n != 0 {
		t.Errorf("ten visits left %d callbacks registered, want 0", n)
	}
}

// One wrapper reused across visits must not accumulate either, because
// the doc comment promises that and a caller may well hold one.
func TestHandoffTermReusedWrapperDoesNotAccumulate(t *testing.T) {
	inner := &detachableTerm{}
	h := NewHandoffTerm(inner)

	for i := 0; i < 10; i++ {
		h.OnResize(func() {})
		h.Release()
	}

	if n := inner.callbacks(); n != 0 {
		t.Errorf("ten visits on one reused wrapper left %d callbacks, want 0", n)
	}
}

// Release is safe with nothing registered and safe more than once. The
// handoff defers it, so it runs on paths where Start failed.
func TestHandoffTermReleaseIsIdempotent(t *testing.T) {
	inner := &detachableTerm{}
	h := NewHandoffTerm(inner)

	h.Release()

	fires := 0
	h.OnResize(func() { fires++ })
	h.Release()
	h.Release()

	inner.fire()
	if fires != 0 {
		t.Errorf("callback fired %d times after two Releases, want 0", fires)
	}
	if n := inner.callbacks(); n != 0 {
		t.Errorf("%d callbacks left registered, want 0", n)
	}
}

// A nil callback is dropped at registration. Recording it would panic on
// a resize, on the signal goroutine, far from whoever passed it.
func TestHandoffTermNilCallbackIsDropped(t *testing.T) {
	inner := &detachableTerm{}
	h := NewHandoffTerm(inner)

	h.OnResize(nil)

	if n := inner.callbacks(); n != 0 {
		t.Errorf("a nil callback was registered %d times, want 0", n)
	}
	inner.fire() // must not panic
	h.Release()
}

// A terminal without the detach upgrade still works. The callback
// registers and stays for the life of the process, which is what that
// terminal does today, so the wrapper makes it no worse.
func TestHandoffTermWithoutTheDetachUpgrade(t *testing.T) {
	inner := &plainTerm{}
	h := NewHandoffTerm(inner)

	fires := 0
	h.OnResize(func() { fires++ })

	inner.fire()
	if fires != 1 {
		t.Fatalf("callback fired %d times, want 1; it did not register at all", fires)
	}

	h.Release() // must not panic
	inner.fire()
	if fires != 2 {
		t.Errorf("callback fired %d times, want 2: without the upgrade there is nothing to detach with", fires)
	}
}

// Everything the wrapper does not override reaches the terminal
// underneath. A guest that could not write or measure the screen would
// paint nothing.
func TestHandoffTermPassesThroughTheRest(t *testing.T) {
	inner := &detachableTerm{}
	h := NewHandoffTerm(inner)

	n, err := h.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Write = (%d, %v), want (5, nil)", n, err)
	}
	inner.mu.Lock()
	written := inner.written
	inner.mu.Unlock()
	if written != 5 {
		t.Errorf("%d bytes reached the host terminal, want 5", written)
	}

	if cols, rows := h.Size(); cols != 80 || rows != 24 {
		t.Errorf("Size = (%d, %d), want (80, 24)", cols, rows)
	}
	if _, err := h.ReadByte(); err != io.EOF {
		t.Errorf("ReadByte error = %v, want io.EOF from the host terminal", err)
	}
	if err := h.SetNonblock(true); err != nil {
		t.Errorf("SetNonblock: %v", err)
	}
}

// The same ten visits over a real *ProcTerm, which is what the handoff
// actually wraps. This is the criterion in the terms the ticket states
// it: no duplicate callbacks and no SIGWINCH goroutines accumulated.
func TestHandoffTermTenVisitsOverProcTerm(t *testing.T) {
	p := NewProcTerm()

	// Register once first, so the single handler goroutine is already
	// running and does not count against the delta below.
	p.OnResize(func() {})
	settleGoroutines()
	before := runtime.NumGoroutine()
	baseline := procTermCallbacks(p)

	for i := 0; i < 10; i++ {
		h := NewHandoffTerm(p)
		h.OnResize(func() {})
		h.OnResize(func() {})
		h.Release()
	}

	settleGoroutines()
	if n := procTermCallbacks(p); n != baseline {
		t.Errorf("ten visits left %d callbacks on the ProcTerm, want %d; the guest's repaints outlived it", n, baseline)
	}
	if delta := runtime.NumGoroutine() - before; delta > 5 {
		t.Errorf("ten visits added %d goroutines, want at most 5", delta)
	}
}

func procTermCallbacks(p *ProcTerm) int {
	p.resizeMu.Lock()
	defer p.resizeMu.Unlock()
	return len(p.resizeCBs)
}
