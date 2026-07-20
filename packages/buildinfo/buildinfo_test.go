package buildinfo

import "testing"

func TestSetGetRoundTrip(t *testing.T) {
	// The build identity is a process-global, so this test mutates state that
	// outlives it. Restore what we found — zero on a normal run — or a REPEAT run
	// (`go test -count=2`, which is how the runner-contention reproduction
	// exercises the suite) inherits the previous iteration's value and fails the
	// zero assertion below. The failure reads like a real bug and is not one; the
	// endpoint registry had the same self-collision, fixed the same way.
	orig := Get()
	t.Cleanup(func() { Set(orig) })

	// Zero before any Set (Get is safe on the unwritten global).
	if got := Get(); got != (Info{}) {
		t.Fatalf("pre-Set Get = %+v, want zero", got)
	}

	want := Info{Version: "0.120.1", Commit: "8f5bd80", Date: "2026-07-12T06:30:32Z"}
	Set(want)
	if got := Get(); got != want {
		t.Errorf("Get = %+v, want %+v", got, want)
	}

	// Set is last-write-wins (a re-exec / second startup overwrites).
	next := Info{Version: "0.120.2"}
	Set(next)
	if got := Get(); got != next {
		t.Errorf("after re-Set, Get = %+v, want %+v", got, next)
	}
}
