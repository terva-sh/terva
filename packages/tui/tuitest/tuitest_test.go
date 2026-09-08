package tuitest

// FakeTerm's resize registry must behave like ProcTerm's, because a
// test that exercises the detach path against the double proves nothing
// if the double is the only one that gets it right.
//
// That asymmetry is the reason TKT-01M1Z5HX3P went unnoticed. FakeTerm
// always took a mutex around its callback slice and never spawned a
// goroutine, so it was correct while ProcTerm raced and leaked. Tests
// exercised the correct one.

import (
	"sync/atomic"
	"testing"
)

func TestFakeTermResizeInvokesEachCallbackOnce(t *testing.T) {
	f := NewFakeTerm(80, 24)
	var a, b atomic.Int32
	f.OnResize(func() { a.Add(1) })
	f.OnResize(func() { b.Add(1) })

	f.Resize(100, 30)

	if a.Load() != 1 || b.Load() != 1 {
		t.Errorf("a=%d b=%d, want 1 and 1", a.Load(), b.Load())
	}
	if cols, rows := f.Size(); cols != 100 || rows != 30 {
		t.Errorf("Size() = %d,%d, want 100,30", cols, rows)
	}
}

func TestFakeTermOnResizeDetach(t *testing.T) {
	f := NewFakeTerm(80, 24)
	var kept, dropped atomic.Int32
	f.OnResize(func() { kept.Add(1) })
	detach := f.OnResizeDetach(func() { dropped.Add(1) })

	f.Resize(90, 25)
	if kept.Load() != 1 || dropped.Load() != 1 {
		t.Fatalf("before detach: kept=%d dropped=%d, want 1 and 1", kept.Load(), dropped.Load())
	}

	detach()
	detach() // twice must be safe
	f.Resize(100, 30)

	if kept.Load() != 2 {
		t.Errorf("kept callback fired %d times, want 2", kept.Load())
	}
	if dropped.Load() != 1 {
		t.Errorf("detached callback fired %d times, want 1", dropped.Load())
	}
}

// FakeTerm offers the same optional upgrade a caller type-asserts for
// on ProcTerm. If this stops compiling, a test that reaches the detach
// path through the interface can no longer use the double.
func TestFakeTermExposesDetachUpgrade(t *testing.T) {
	var term any = NewFakeTerm(80, 24)
	if _, ok := term.(interface{ OnResizeDetach(func()) func() }); !ok {
		t.Fatal("FakeTerm no longer offers OnResizeDetach; it has drifted from ProcTerm")
	}
}
