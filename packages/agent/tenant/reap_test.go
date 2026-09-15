package tenant

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func startedChild(t *testing.T, s *Supervisor, subject string) *Child {
	t.Helper()
	c, err := s.Start(context.Background(), enrol(t, subject))
	if err != nil {
		t.Fatalf("start %s: %v", subject, err)
	}
	return c
}

// The rule that matters most: a child somebody is attached to is never idle,
// however long they have been reading. "Idle" means nobody is connected, not
// nobody has typed — otherwise a long think costs a websocket.
func TestAHeldChildIsNeverReaped(t *testing.T) {
	skipOnWindows(t)
	s := newTestSupervisor(t, isolating{})
	c := startedChild(t, s, "sub-a")

	release := c.Hold()
	// Backdate well past any timeout; the hold must win regardless.
	c.mu.Lock()
	c.lastUsed = time.Now().Add(-24 * time.Hour)
	c.mu.Unlock()

	if n := s.ReapIdle(time.Nanosecond); n != 0 {
		t.Fatalf("reaped %d held children", n)
	}
	if !c.Alive() {
		t.Fatal("a held child was stopped")
	}

	// The control: once released, the same child IS reapable — so the test
	// above is about the hold and not about the reaper being broken.
	release()
	c.mu.Lock()
	c.lastUsed = time.Now().Add(-24 * time.Hour)
	c.mu.Unlock()
	if n := s.ReapIdle(time.Nanosecond); n != 1 {
		t.Fatalf("CONTROL: reaped %d after release, want 1", n)
	}
	if c.Alive() {
		t.Error("the released child was not stopped")
	}
}

// Releasing restarts the idle clock: the countdown begins when the last
// connection drops, not when it was opened.
func TestTheIdleClockStartsWhenTheLastConnectionDrops(t *testing.T) {
	skipOnWindows(t)
	s := newTestSupervisor(t, isolating{})
	c := startedChild(t, s, "sub-a")

	c.mu.Lock()
	c.lastUsed = time.Now().Add(-24 * time.Hour)
	c.mu.Unlock()

	release := c.Hold()
	release()

	if n := s.ReapIdle(time.Hour); n != 0 {
		t.Fatalf("reaped %d — a just-released child was treated as 24h idle", n)
	}
	if !c.Alive() {
		t.Error("a just-released child was stopped")
	}
}

// Reaping stops a PROCESS. It must not touch the home, the record, or anything
// the tenant would miss — the next request starts a fresh daemon over the same
// data. This is the property that lets it run unattended on a timer.
func TestReapingLeavesTheTenantsDataAlone(t *testing.T) {
	skipOnWindows(t)
	s := newTestSupervisor(t, isolating{})
	rec := enrol(t, "sub-a")
	c, err := s.Start(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(c.Home, "work.txt")
	if err := os.WriteFile(marker, []byte("a conversation"), 0o600); err != nil {
		t.Fatal(err)
	}

	c.mu.Lock()
	c.lastUsed = time.Now().Add(-time.Hour)
	c.mu.Unlock()
	if n := s.ReapIdle(time.Minute); n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}

	if b, err := os.ReadFile(marker); err != nil || string(b) != "a conversation" {
		t.Fatalf("reaping touched the tenant's home: %q %v", b, err)
	}
	// And the tenant can come straight back to it.
	again, err := s.Start(context.Background(), rec)
	if err != nil {
		t.Fatalf("a reaped tenant could not restart: %v", err)
	}
	if again.Home != c.Home {
		t.Errorf("restarted into a different home: %q then %q", c.Home, again.Home)
	}
	if b, _ := os.ReadFile(filepath.Join(again.Home, "work.txt")); string(b) != "a conversation" {
		t.Error("the restarted environment does not see the earlier work")
	}
}

func TestAnIdleTimeoutOfZeroReapsNothing(t *testing.T) {
	skipOnWindows(t)
	s := newTestSupervisor(t, isolating{})
	c := startedChild(t, s, "sub-a")
	c.mu.Lock()
	c.lastUsed = time.Now().Add(-24 * time.Hour)
	c.mu.Unlock()

	if n := s.ReapIdle(0); n != 0 {
		t.Errorf("reaped %d with reaping disabled", n)
	}
	if !c.Alive() {
		t.Error("a child was stopped with reaping disabled")
	}
}

// A child that died on its own must not be left in the registry pretending to
// serve someone.
func TestReapingForgetsChildrenThatAlreadyExited(t *testing.T) {
	skipOnWindows(t)
	s := newTestSupervisor(t, isolating{})
	c := startedChild(t, s, "sub-a")
	c.shutdown()

	s.ReapIdle(time.Hour)
	if n := len(s.Running()); n != 0 {
		t.Errorf("%d dead children still registered", n)
	}
}

func TestRunReaperStopsWithItsContext(t *testing.T) {
	skipOnWindows(t)
	s := newTestSupervisor(t, isolating{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.RunReaper(ctx, time.Hour, 10*time.Millisecond); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the reaper outlived its context")
	}
}
