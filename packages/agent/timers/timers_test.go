package timers

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// fakeClock is the test's time. Nothing here reads the wall clock to make
// progress: a test moves the time, and the service acts on what it reads.
type fakeClock struct {
	mu      sync.Mutex
	cond    *sync.Cond
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func newFakeClock(now time.Time) *fakeClock {
	c := &fakeClock{now: now}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, fakeWaiter{c.now.Add(d), ch})
	c.cond.Broadcast()
	return ch
}

// Advance moves the time forward and wakes every sleeper whose time came.
func (c *fakeClock) Advance(d time.Duration) { c.Set(c.Now().Add(d)) }

// Set moves the time to t, forward or back.
func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
	keep := c.waiters[:0]
	for _, w := range c.waiters {
		if w.at.After(t) {
			keep = append(keep, w)
			continue
		}
		w.ch <- t
	}
	c.waiters = keep
}

// awaitSleeper blocks until the service's loop sleeps on this clock.
func (c *fakeClock) awaitSleeper() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.waiters) == 0 {
		c.cond.Wait()
	}
}

var t0 = time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)

// recorder collects the firings of one owner.
type recorder struct {
	mu  sync.Mutex
	got []Firing
}

func (r *recorder) handle(f Firing) {
	r.mu.Lock()
	r.got = append(r.got, f)
	r.mu.Unlock()
}

func (r *recorder) firings() []Firing {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Firing(nil), r.got...)
}

func open(t *testing.T, path string, clock Clock) *Service {
	t.Helper()
	s, err := Open(path, Options{Clock: clock, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func every(d time.Duration) Schedule { return Schedule{Every: Duration(d), Anchor: t0} }

func mustSet(t *testing.T, s *Service, tm Timer) Timer {
	t.Helper()
	out, err := s.Set(tm)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A one-time timer fires its owner once, at its time, and is gone after.
func TestAOneTimeTimerFiresOnceAndIsRemoved(t *testing.T) {
	clock := newFakeClock(t0)
	s := open(t, filepath.Join(testsupport.TempDir(t), "timers.json"), clock)
	var r recorder
	s.Handle("wake", r.handle)
	mustSet(t, s, Timer{Owner: "wake", ID: "a", Schedule: Schedule{At: t0.Add(20 * time.Minute)}, Data: json.RawMessage(`{"chain":"c1"}`)})

	if wait := s.Poll(); wait != pollMax {
		t.Fatalf("Poll waits %s before the timer is near, want the poll limit %s", wait, pollMax)
	}
	clock.Advance(19*time.Minute + 45*time.Second)
	if wait := s.Poll(); wait != 15*time.Second || len(r.firings()) != 0 {
		t.Fatalf("15 seconds early: Poll waits %s with %d firings, want 15s and none", wait, len(r.firings()))
	}
	clock.Advance(15 * time.Second)
	s.Poll()
	got := r.firings()
	if len(got) != 1 || !got[0].Due.Equal(t0.Add(20*time.Minute)) || got[0].Missed != 0 || got[0].Late != 0 || got[0].Redelivered {
		t.Fatalf("the firings are %+v, want one on time", got)
	}
	if string(got[0].Timer.Data) != `{"chain":"c1"}` {
		t.Fatalf("the handler got the data %s, want what the owner set", got[0].Timer.Data)
	}
	if list := s.List(); len(list) != 0 {
		t.Fatalf("a fired one-time timer is still listed: %+v", list)
	}
	clock.Advance(time.Hour)
	s.Poll()
	if n := len(r.firings()); n != 1 {
		t.Fatalf("the one-time timer fired %d times", n)
	}
}

// A recurring timer fires at each window. A late firing does not move the
// windows after it, because they count from the anchor.
func TestARecurringTimerKeepsItsWindows(t *testing.T) {
	clock := newFakeClock(t0)
	s := open(t, filepath.Join(testsupport.TempDir(t), "timers.json"), clock)
	var r recorder
	s.Handle("watch", r.handle)
	mustSet(t, s, Timer{Owner: "watch", ID: "ci", Schedule: every(15 * time.Minute)})

	clock.Advance(15 * time.Minute)
	s.Poll()
	// The loop looked four minutes late.
	clock.Advance(19 * time.Minute)
	s.Poll()
	clock.Advance(11 * time.Minute)
	s.Poll()

	got := r.firings()
	if len(got) != 3 {
		t.Fatalf("the timer fired %d times, want 3", len(got))
	}
	for i, want := range []time.Duration{15 * time.Minute, 30 * time.Minute, 45 * time.Minute} {
		if !got[i].Due.Equal(t0.Add(want)) || got[i].Missed != 0 {
			t.Errorf("firing %d is for %s with %d missed, want %s and none", i, got[i].Due.Sub(t0), got[i].Missed, want)
		}
	}
	if got[1].Late != 4*time.Minute {
		t.Errorf("the late firing says it is %s late, want 4m", got[1].Late)
	}
}

// A timer set before a restart fires after it.
func TestATimerSurvivesARestart(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "timers.json")
	clock := newFakeClock(t0)
	first := open(t, path, clock)
	mustSet(t, first, Timer{Owner: "wake", ID: "a", Label: "look at the build", Schedule: Schedule{At: t0.Add(time.Hour)}})
	mustSet(t, first, Timer{Owner: "watch", ID: "ci", Schedule: every(15 * time.Minute)})

	// The first daemon is gone. Nothing of it is used again.
	clock.Advance(10 * time.Minute)
	second := open(t, path, clock)
	var wake, watch recorder
	second.Handle("wake", wake.handle)
	second.Handle("watch", watch.handle)
	if list := second.List(); len(list) != 2 || list[1].Label != "look at the build" {
		t.Fatalf("after the restart the service lists %+v, want both timers", list)
	}
	clock.Advance(50 * time.Minute)
	second.Poll()
	if n := len(wake.firings()); n != 1 {
		t.Fatalf("the one-time timer fired %d times after the restart, want 1", n)
	}
	if n := len(watch.firings()); n != 1 {
		t.Fatalf("the recurring timer fired %d times after the restart, want 1", n)
	}
}

// 🚨 A gap never replays. After downtime a recurring timer fires one time,
// for the latest window, and the firing carries the count of the windows
// that passed and the time of the first.
func TestDowntimeFiresOnceAndCountsTheMissedWindows(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "timers.json")
	clock := newFakeClock(t0)
	mustSet(t, open(t, path, clock), Timer{Owner: "watch", ID: "ci", Schedule: every(15 * time.Minute)})

	// Down for 3 hours and 5 minutes: the windows from 09:15 to 12:00 passed.
	clock.Advance(3*time.Hour + 5*time.Minute)
	s := open(t, path, clock)
	var r recorder
	s.Handle("watch", r.handle)
	s.Poll()
	s.Poll()

	got := r.firings()
	if len(got) != 1 {
		t.Fatalf("the timer fired %d times after the gap, want exactly 1", len(got))
	}
	f := got[0]
	if !f.Due.Equal(t0.Add(3*time.Hour)) || f.Missed != 11 || !f.First.Equal(t0.Add(15*time.Minute)) || f.Late != 5*time.Minute {
		t.Fatalf("the firing is for %s with %d missed, first %s, %s late; want 12:00, 11, 09:15, 5m",
			f.Due.Format("15:04"), f.Missed, f.First.Format("15:04"), f.Late)
	}
	if tm, _ := s.Get("watch", "ci"); !tm.Next.Equal(t0.Add(3*time.Hour + 15*time.Minute)) {
		t.Fatalf("the next window is %s, want 12:15", tm.Next.Format("15:04"))
	}
}

// A one-time timer whose time passed during downtime fires once at start,
// and says how late it is.
func TestAOneTimeTimerMissedDuringDowntimeFiresAtStart(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "timers.json")
	clock := newFakeClock(t0)
	mustSet(t, open(t, path, clock), Timer{Owner: "wake", ID: "a", Schedule: Schedule{At: t0.Add(20 * time.Minute)}})

	clock.Advance(26 * time.Hour)
	s := open(t, path, clock)
	var r recorder
	s.Handle("wake", r.handle)
	s.Poll()
	s.Poll()
	got := r.firings()
	if len(got) != 1 || got[0].Late != 26*time.Hour-20*time.Minute || got[0].Missed != 0 {
		t.Fatalf("the firings are %+v, want one that is 25h40m late", got)
	}
}

// 🚨 A clock that moves back delays a timer and never repeats one. A board
// with no battery clock starts on a stale time, and a service that started
// its schedules again from that time would run the morning's work twice.
func TestAClockThatMovesBackNeverRepeatsAWindow(t *testing.T) {
	clock := newFakeClock(t0)
	s := open(t, filepath.Join(testsupport.TempDir(t), "timers.json"), clock)
	var r recorder
	s.Handle("brief", r.handle)
	sched, err := ParseSchedule("daily 09:30 UTC", t0)
	if err != nil {
		t.Fatal(err)
	}
	mustSet(t, s, Timer{Owner: "brief", ID: "morning", Schedule: sched})

	clock.Advance(30 * time.Minute)
	s.Poll()
	if n := len(r.firings()); n != 1 {
		t.Fatalf("the brief fired %d times at 09:30, want 1", n)
	}
	// The clock goes back to 09:00, and then passes 09:30 again.
	clock.Set(t0)
	s.Poll()
	clock.Advance(45 * time.Minute)
	s.Poll()
	if n := len(r.firings()); n != 1 {
		t.Fatalf("after the clock moved back the brief fired %d times, want still 1", n)
	}
	clock.Set(t0.Add(24*time.Hour + 30*time.Minute))
	s.Poll()
	if got := r.firings(); len(got) != 2 || got[1].Missed != 0 {
		t.Fatalf("the next day's brief: %d firings, want 2 with none missed", len(got))
	}
}

// 🚨 A firing reaches its handler at least once. A daemon that stops while
// the handler runs left a mark in the file, and the next daemon delivers the
// firing again and says that it is a second delivery.
func TestAFiringInFlightIsDeliveredAgainAfterACrash(t *testing.T) {
	dir := testsupport.TempDir(t)
	path, crashed := filepath.Join(dir, "timers.json"), filepath.Join(dir, "crashed.json")
	clock := newFakeClock(t0)
	s := open(t, path, clock)
	// The handler copies the file as it stands while it runs. That copy is
	// what a daemon killed at this moment would leave behind.
	s.Handle("wake", func(Firing) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(crashed, b, 0o600); err != nil {
			t.Error(err)
		}
	})
	mustSet(t, s, Timer{Owner: "wake", ID: "a", Schedule: Schedule{At: t0.Add(20 * time.Minute)}})
	clock.Advance(20 * time.Minute)
	s.Poll()

	clock.Advance(5 * time.Minute)
	after := open(t, crashed, clock)
	var r recorder
	after.Handle("wake", r.handle)
	after.Poll()
	after.Poll()
	got := r.firings()
	if len(got) != 1 || !got[0].Redelivered || !got[0].Due.Equal(t0.Add(20*time.Minute)) {
		t.Fatalf("after the crash the firings are %+v, want the one firing again, marked redelivered", got)
	}
	if list := after.List(); len(list) != 0 {
		t.Fatalf("the redelivered one-time timer is still listed: %+v", list)
	}

	// The daemon that did not crash left no mark, so nothing comes twice.
	clean := open(t, path, clock)
	var none recorder
	clean.Handle("wake", none.handle)
	clean.Poll()
	if n := len(none.firings()); n != 0 {
		t.Fatalf("a finished firing was delivered %d more time(s)", n)
	}
}

// 🚨 A firing that cannot be written down does not happen. The window stays
// due, the service says what is wrong, and the firing comes when the file
// writes again.
func TestAFiringWaitsForAFileThatWrites(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "timers.json")
	clock := newFakeClock(t0)
	s := open(t, path, clock)
	var r recorder
	s.Handle("watch", r.handle)
	mustSet(t, s, Timer{Owner: "watch", ID: "ci", Schedule: every(15 * time.Minute)})

	// A directory that is not empty stands where the file was, so the rename
	// that replaces the file fails for every user, root included.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "in-the-way"), 0o700); err != nil {
		t.Fatal(err)
	}
	clock.Advance(15 * time.Minute)
	s.Poll()
	if n := len(r.firings()); n != 0 {
		t.Fatalf("the timer fired %d time(s) with nothing written down", n)
	}
	if s.Problem() == "" {
		t.Fatal("the service reports no problem while its file does not write")
	}
	if list := s.List(); len(list) != 1 || list[0].Problem == "" || !list[0].Next.Equal(t0.Add(15*time.Minute)) {
		t.Fatalf("the list says %+v, want the timer still due at 09:15 with the problem", list)
	}

	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	clock.Advance(20 * time.Minute)
	s.Poll()
	got := r.firings()
	if len(got) != 1 || !got[0].Due.Equal(t0.Add(30*time.Minute)) || got[0].Missed != 1 {
		t.Fatalf("once the file writes the firings are %+v, want one for 09:30 with 1 missed", got)
	}
	if s.Problem() != "" {
		t.Fatalf("the problem stays after the file wrote: %s", s.Problem())
	}
}

// A timer whose owner has no handler waits. The list says why, and the timer
// fires when the handler arrives.
func TestATimerWaitsForItsOwnersHandler(t *testing.T) {
	clock := newFakeClock(t0)
	s := open(t, filepath.Join(testsupport.TempDir(t), "timers.json"), clock)
	mustSet(t, s, Timer{Owner: "routine", ID: "r1", Schedule: Schedule{At: t0.Add(time.Minute)}})
	clock.Advance(time.Hour)
	s.Poll()
	list := s.List()
	if len(list) != 1 || !strings.Contains(list[0].Problem, "routine") {
		t.Fatalf("the list says %+v, want the timer held with the owner named", list)
	}
	var r recorder
	s.Handle("routine", r.handle)
	s.Poll()
	if n := len(r.firings()); n != 1 {
		t.Fatalf("the timer fired %d times once its handler arrived, want 1", n)
	}
}

// A handler may set its own timer again, or cancel it. The service must not
// write its end-of-firing bookkeeping over the record the handler left.
func TestAHandlerMayReplaceOrCancelItsOwnTimer(t *testing.T) {
	clock := newFakeClock(t0)
	s := open(t, filepath.Join(testsupport.TempDir(t), "timers.json"), clock)
	s.Handle("wake", func(f Firing) {
		// A one-time timer that sets itself again, as a follow-up does.
		if _, err := s.Set(Timer{Owner: "wake", ID: f.Timer.ID, Schedule: Schedule{At: f.Due.Add(time.Hour)}}); err != nil {
			t.Error(err)
		}
	})
	s.Handle("watch", func(f Firing) {
		if _, err := s.Cancel("watch", f.Timer.ID); err != nil {
			t.Error(err)
		}
	})
	mustSet(t, s, Timer{Owner: "wake", ID: "a", Schedule: Schedule{At: t0.Add(time.Minute)}})
	mustSet(t, s, Timer{Owner: "watch", ID: "ci", Schedule: every(time.Minute)})
	clock.Advance(time.Minute)
	s.Poll()

	tm, ok := s.Get("wake", "a")
	if !ok || !tm.Next.Equal(t0.Add(time.Hour+time.Minute)) || tm.Pending != nil {
		t.Fatalf("the timer the handler set again is %+v (found %v), want it waiting for 10:01 with no mark", tm, ok)
	}
	if _, ok := s.Get("watch", "ci"); ok {
		t.Fatal("the timer its handler cancelled is back")
	}
}

// The loop sleeps until the next window and fires it, with no wall clock.
func TestTheLoopFiresWhenTheClockReachesAWindow(t *testing.T) {
	clock := newFakeClock(t0)
	s := open(t, filepath.Join(testsupport.TempDir(t), "timers.json"), clock)
	fired := make(chan Firing, 4)
	s.Handle("wake", func(f Firing) { fired <- f })
	mustSet(t, s, Timer{Owner: "wake", ID: "a", Schedule: Schedule{At: t0.Add(10 * time.Second)}})

	s.Start(context.Background())
	t.Cleanup(s.Stop)
	clock.awaitSleeper()
	clock.Advance(10 * time.Second)
	if f := <-fired; !f.Due.Equal(t0.Add(10 * time.Second)) {
		t.Fatalf("the loop fired for %s", f.Due)
	}

	// A timer set while the loop sleeps wakes it.
	mustSet(t, s, Timer{Owner: "wake", ID: "b", Schedule: Schedule{At: clock.Now()}})
	if f := <-fired; f.Timer.ID != "b" {
		t.Fatalf("the loop fired %s, want the timer set while it slept", f.Timer.ID)
	}
}

// A host shows the timers to a person, so it must hear when the list is out
// of date: a timer came, a timer went, or a firing moved a next window.
func TestTheServiceReportsEachChange(t *testing.T) {
	clock := newFakeClock(t0)
	var changes atomic.Int32
	s, err := Open(filepath.Join(testsupport.TempDir(t), "timers.json"), Options{
		Clock: clock, Logf: t.Logf, OnChange: func() { changes.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	s.Handle("wake", rec.handle)

	mustSet(t, s, Timer{Owner: "wake", ID: "a", Schedule: every(time.Hour)})
	if got := changes.Load(); got != 1 {
		t.Fatalf("a new timer reported %d changes, want 1", got)
	}
	s.Poll()
	if got := changes.Load(); got != 1 {
		t.Fatalf("a poll that fired nothing reported a change: %d", got)
	}
	clock.Advance(time.Hour)
	s.Poll()
	if got := changes.Load(); got != 2 {
		t.Fatalf("a firing reported %d changes in all, want 2", got)
	}
	if ok, err := s.Cancel("wake", "a"); err != nil || !ok {
		t.Fatalf("Cancel = %v, %v", ok, err)
	}
	if got := changes.Load(); got != 3 {
		t.Fatalf("a cancel reported %d changes in all, want 3", got)
	}
	if ok, _ := s.Cancel("wake", "a"); ok {
		t.Fatal("a second cancel found the timer")
	}
	if got := changes.Load(); got != 3 {
		t.Fatalf("a cancel of nothing reported a change: %d", got)
	}
}

// A second Poll beside the first must not read the first one's mark as a
// firing that a dead daemon left. The handler here holds the first Poll open
// while a second one runs to its end.
func TestTwoPollsAtOnceDeliverAFiringOnce(t *testing.T) {
	clock := newFakeClock(t0)
	s := open(t, filepath.Join(testsupport.TempDir(t), "timers.json"), clock)
	rec := &recorder{}
	entered, release := make(chan struct{}, 2), make(chan struct{})
	s.Handle("wake", func(f Firing) {
		rec.handle(f)
		entered <- struct{}{}
		<-release
	})
	mustSet(t, s, Timer{Owner: "wake", ID: "a", Schedule: Schedule{At: t0.Add(time.Minute)}})
	clock.Advance(time.Minute)

	first := make(chan struct{})
	go func() { defer close(first); s.Poll() }()
	<-entered
	// The handler is running now, and its firing is marked in the file.
	done := make(chan struct{})
	go func() { defer close(done); s.Poll() }()
	select {
	case <-done:
	case <-entered:
		close(release)
		<-done
		<-first
		t.Fatalf("the second poll delivered the firing again: %+v", rec.firings())
	}
	close(release)
	<-first

	got := rec.firings()
	if len(got) != 1 || got[0].Redelivered {
		t.Fatalf("firings = %+v, want one that is not a second delivery", got)
	}
	if _, ok := s.Get("wake", "a"); ok {
		t.Fatal("the one-time timer stayed after its firing")
	}
}

// A person who has the list open must see the service go out of order and
// come back. A save that fails changes what Problem returns, and the host
// hears it once, not at every poll that fails the same way.
func TestTheServiceReportsAProblemWithItsFileOnce(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "timers.json")
	clock := newFakeClock(t0)
	var changes atomic.Int32
	s, err := Open(path, Options{Clock: clock, Logf: t.Logf, OnChange: func() { changes.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	var r recorder
	s.Handle("watch", r.handle)
	mustSet(t, s, Timer{Owner: "watch", ID: "ci", Schedule: every(15 * time.Minute)})
	changes.Store(0)

	// The same obstacle as TestAFiringWaitsForAFileThatWrites.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "in-the-way"), 0o700); err != nil {
		t.Fatal(err)
	}
	clock.Advance(15 * time.Minute)
	s.Poll()
	if s.Problem() == "" {
		t.Fatal("the service reports no problem while its file does not write")
	}
	if got := changes.Load(); got != 1 {
		t.Fatalf("the problem reported %d changes, want 1", got)
	}
	s.Poll()
	if got := changes.Load(); got != 1 {
		t.Fatalf("the same problem at the next poll reported a change: %d in all", got)
	}
	// A Set that cannot write is refused, and the problem is not news.
	if _, err := s.Set(Timer{Owner: "watch", ID: "other", Schedule: every(time.Hour)}); err == nil {
		t.Fatal("Set kept a timer that the file does not hold")
	}
	if got := changes.Load(); got != 1 {
		t.Fatalf("a refused Set reported a change: %d in all", got)
	}

	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	s.Poll()
	if s.Problem() != "" || len(r.firings()) != 1 {
		t.Fatalf("once the file writes: problem %q, firings %+v", s.Problem(), r.firings())
	}
	if got := changes.Load(); got != 2 {
		t.Fatalf("the recovery and its firing reported %d changes in all, want 2", got)
	}
}

// A Set whose file does not write is the first sign of the problem when no
// timer is due, and the host must hear that too.
func TestARefusedSetReportsTheProblem(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "timers.json")
	var changes atomic.Int32
	s, err := Open(path, Options{Clock: newFakeClock(t0), Logf: t.Logf, OnChange: func() { changes.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "in-the-way"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set(Timer{Owner: "watch", ID: "ci", Schedule: every(time.Hour)}); err == nil {
		t.Fatal("Set kept a timer that the file does not hold")
	}
	if s.Problem() == "" || changes.Load() != 1 {
		t.Fatalf("after a refused Set: problem %q, %d changes, want a problem and 1", s.Problem(), changes.Load())
	}
}

// A timer whose handler another Poll runs is not this Poll's to wait for. A
// Poll that counted it would return no wait at all, and the loop would spin
// for as long as that handler ran.
func TestAPollBesideARunningHandlerWaits(t *testing.T) {
	clock := newFakeClock(t0)
	s := open(t, filepath.Join(testsupport.TempDir(t), "timers.json"), clock)
	rec := &recorder{}
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var held atomic.Bool
	s.Handle("wake", func(f Firing) {
		rec.handle(f)
		if held.CompareAndSwap(false, true) {
			entered <- struct{}{}
			<-release
		}
	})
	mustSet(t, s, Timer{Owner: "wake", ID: "a", Schedule: every(time.Minute)})
	<-s.kick // Set woke a loop that this test does not run.
	clock.Advance(time.Minute)

	first := make(chan struct{})
	go func() { defer close(first); s.Poll() }()
	<-entered
	// The next window comes while the handler still runs.
	clock.Advance(time.Minute)
	if wait := s.Poll(); wait <= 0 {
		t.Fatalf("a poll beside the running handler returned the wait %v, want a real one", wait)
	}
	if n := len(rec.firings()); n != 1 {
		t.Fatalf("%d firings while the first handler runs, want 1", n)
	}
	close(release)
	<-first

	// The poll that ran the handler tells the loop to look again, so the
	// window that came meanwhile does not wait out a sleep.
	select {
	case <-s.kick:
	default:
		t.Fatal("the poll that ran the handler did not wake the loop")
	}
	s.Poll()
	got := rec.firings()
	if len(got) != 2 || !got[1].Due.Equal(t0.Add(2*time.Minute)) {
		t.Fatalf("firings = %+v, want a second one for 09:02", got)
	}
}

// A caller that builds a Schedule by hand may give the times in any order.
// The service reads them in order, so it must put them in order, or the
// first window of the day is skipped.
func TestSetPutsTimesOfDayInOrder(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "timers.json")
	dawn := time.Date(2026, 11, 17, 6, 0, 0, 0, time.UTC) // a Tuesday
	s := open(t, path, newFakeClock(dawn))
	got := mustSet(t, s, Timer{Owner: "routine", ID: "brief", Schedule: Schedule{
		Daily: []ClockTime{19 * 60, 7*60 + 30, 19 * 60},
		Days:  []time.Weekday{time.Friday, time.Tuesday, time.Friday},
		Zone:  "UTC",
	}})
	if want := time.Date(2026, 11, 17, 7, 30, 0, 0, time.UTC); !got.Next.Equal(want) {
		t.Fatalf("next = %s, want the morning window %s", got.Next, want)
	}
	if text := got.Schedule.String(); text != "tue,fri 07:30,19:00 UTC" {
		t.Fatalf("schedule = %q, want the times and the days in order, each once", text)
	}

	// A person can write the file by hand, in any order.
	if _, err := s.Cancel("routine", "brief"); err != nil {
		t.Fatal(err)
	}
	file := `{"version":1,"timers":[{"owner":"routine","id":"brief","created":"2026-11-17T06:00:00Z",` +
		`"next":"2026-11-17T07:30:00Z","schedule":{"daily":["19:00","07:30"],"zone":"UTC"}}]}`
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	clock := newFakeClock(time.Date(2026, 11, 18, 6, 0, 0, 0, time.UTC))
	s2 := open(t, path, clock)
	var r recorder
	s2.Handle("routine", r.handle)
	s2.Poll()
	// Two windows passed, 07:30 and 19:00. After them the next is tomorrow
	// morning, and a schedule out of order would say tomorrow evening.
	tm, _ := s2.Get("routine", "brief")
	if want := time.Date(2026, 11, 18, 7, 30, 0, 0, time.UTC); !tm.Next.Equal(want) {
		t.Fatalf("after the gap, next = %s, want %s", tm.Next, want)
	}
	if got := r.firings(); len(got) != 1 || got[0].Missed != 1 {
		t.Fatalf("firings = %+v, want one that missed 1 window", got)
	}
}

// A zone can stop loading after a timer was set, when the daemon moves to a
// machine with less zone data. That costs the one timer, which waits and says
// why. Every other timer in the file still fires.
func TestAZoneThisMachineDoesNotKnowStopsOneTimer(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "timers.json")
	file := `{"version":1,"timers":[` +
		`{"owner":"routine","id":"brief","created":"2026-11-17T06:00:00Z","next":"2026-11-17T07:30:00Z",` +
		`"schedule":{"daily":["07:30"],"zone":"Nowhere/Missing"}},` +
		`{"owner":"routine","id":"check","created":"2026-11-17T06:00:00Z","next":"2026-11-17T09:15:00Z",` +
		`"schedule":{"every":"15m0s","anchor":"2026-11-17T09:00:00Z"}}]}`
	if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	clock := newFakeClock(t0.Add(15 * time.Minute))
	s := open(t, path, clock)
	var r recorder
	s.Handle("routine", r.handle)
	wait := s.Poll()

	got := r.firings()
	if len(got) != 1 || got[0].Timer.ID != "check" {
		t.Fatalf("firings = %+v, want the interval timer alone", got)
	}
	// The brief is past due and cannot fire. A poll that counted it would
	// return no wait at all.
	if wait <= 0 {
		t.Fatalf("the wait is %v with a timer that cannot fire, want a real one", wait)
	}
	for _, in := range s.List() {
		switch in.ID {
		case "brief":
			if !strings.Contains(in.Problem, "Nowhere/Missing") {
				t.Errorf("the brief's problem is %q, want it to name the zone", in.Problem)
			}
		case "check":
			if in.Problem != "" {
				t.Errorf("the interval timer has the problem %q", in.Problem)
			}
		}
	}
	// A new timer cannot name such a zone.
	if _, err := s.Set(Timer{Owner: "routine", ID: "new", Schedule: Schedule{Daily: []ClockTime{60}, Zone: "Nowhere/Missing"}}); err == nil {
		t.Fatal("Set took a zone this machine does not know")
	}
}

// A save can fail after the handler returned. The firing is done, and the
// file still holds its mark. With nothing else due, the next poll must write
// the file again, or the problem stays until some other timer fires.
func TestAFailedSaveAfterAHandlerIsTriedAgain(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "timers.json")
	clock := newFakeClock(t0)
	s := open(t, path, clock)
	s.Handle("wake", func(Firing) {
		// The file stops writing while the handler runs, so the mark went
		// down and the save after the handler does not.
		if err := os.Remove(path); err != nil {
			t.Error(err)
		}
		if err := os.MkdirAll(filepath.Join(path, "in-the-way"), 0o700); err != nil {
			t.Error(err)
		}
	})
	mustSet(t, s, Timer{Owner: "wake", ID: "a", Schedule: Schedule{At: t0.Add(time.Minute)}})
	clock.Advance(time.Minute)
	s.Poll()
	if s.Problem() == "" {
		t.Fatal("the service reports no problem after the save failed")
	}
	if _, ok := s.Get("wake", "a"); ok {
		t.Fatal("the one-time timer stayed in memory after its firing")
	}

	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	// Nothing is due. The poll is only the retry.
	s.Poll()
	if s.Problem() != "" {
		t.Fatalf("the problem stays after the file writes again: %s", s.Problem())
	}
	// The file agrees with memory now, so a restart delivers nothing again.
	s2 := open(t, path, clock)
	var r recorder
	s2.Handle("wake", r.handle)
	s2.Poll()
	if got := r.firings(); len(got) != 0 {
		t.Fatalf("after the retry a restart delivered %+v, want nothing", got)
	}
}

// A handler that panics does not stop the service, and its firing counts as
// delivered.
func TestAHandlerThatPanicsDoesNotStopTheService(t *testing.T) {
	clock := newFakeClock(t0)
	s := open(t, filepath.Join(testsupport.TempDir(t), "timers.json"), clock)
	var r recorder
	s.Handle("bad", func(Firing) { panic("boom") })
	s.Handle("good", r.handle)
	mustSet(t, s, Timer{Owner: "bad", ID: "a", Schedule: Schedule{At: t0.Add(time.Minute)}})
	mustSet(t, s, Timer{Owner: "good", ID: "a", Schedule: Schedule{At: t0.Add(time.Minute)}})
	clock.Advance(time.Minute)
	s.Poll()
	if n := len(r.firings()); n != 1 {
		t.Fatalf("the other owner got %d firings after a panic, want 1", n)
	}
	if list := s.List(); len(list) != 0 {
		t.Fatalf("timers remain after the panic: %+v", list)
	}
}

func TestSetRefusesWhatTheServiceCannotKeep(t *testing.T) {
	clock := newFakeClock(t0)
	s := open(t, filepath.Join(testsupport.TempDir(t), "timers.json"), clock)
	at := Schedule{At: t0.Add(time.Hour)}
	for name, tm := range map[string]Timer{
		"an owner with a slash": {Owner: "a/b", ID: "x", Schedule: at},
		"an empty id":           {Owner: "a", ID: "", Schedule: at},
		"no schedule":           {Owner: "a", ID: "x"},
		"two schedules":         {Owner: "a", ID: "x", Schedule: Schedule{At: t0, Every: Duration(time.Hour), Anchor: t0}},
		"a short interval":      {Owner: "a", ID: "x", Schedule: Schedule{Every: Duration(time.Second), Anchor: t0}},
		"data that is not JSON": {Owner: "a", ID: "x", Schedule: at, Data: json.RawMessage(`{`)},
		"too much data":         {Owner: "a", ID: "x", Schedule: at, Data: json.RawMessage(`"` + strings.Repeat("x", MaxPayload) + `"`)},
	} {
		if _, err := s.Set(tm); err == nil {
			t.Errorf("Set accepted %s", name)
		}
	}
	// Filled in memory: each Set writes and syncs the file, and a thousand of
	// those is most of this package's test time.
	s.mu.Lock()
	for i := range MaxTimers {
		tm := Timer{Owner: "a", ID: "t" + strconv.Itoa(i), Schedule: at, Created: t0, Next: at.At}
		s.timers[key(tm.Owner, tm.ID)] = &tm
	}
	s.mu.Unlock()
	if _, err := s.Set(Timer{Owner: "a", ID: "one-more", Schedule: at}); err == nil {
		t.Fatalf("Set accepted timer %d", MaxTimers+1)
	}
	// Replacing one that exists is not one more.
	if _, err := s.Set(Timer{Owner: "a", ID: "t0", Schedule: at}); err != nil {
		t.Fatalf("Set refused to replace a timer at the limit: %v", err)
	}
}

// Open refuses a file it cannot trust and leaves it as it found it, so a
// person can still repair it.
func TestOpenRefusesAFileItCannotReadAndLeavesIt(t *testing.T) {
	dir := testsupport.TempDir(t)
	for name, body := range map[string]string{
		"not JSON":        `{"version": 1, "timers": [`,
		"a later version": `{"version": 2, "timers": []}`,
		"a bad schedule":  `{"version": 1, "timers": [{"owner":"a","id":"x","schedule":{"every":"1s","anchor":"2026-11-17T09:00:00Z"},"created":"2026-11-17T09:00:00Z"}]}`,
		"a timer twice":   `{"version": 1, "timers": [{"owner":"a","id":"x","schedule":{"at":"2026-11-17T09:00:00Z"},"created":"2026-11-17T09:00:00Z"},{"owner":"a","id":"x","schedule":{"at":"2026-11-17T09:00:00Z"},"created":"2026-11-17T09:00:00Z"}]}`,
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path, Options{}); err == nil {
			t.Errorf("Open accepted a file that is %s", name)
		}
		if b, _ := os.ReadFile(path); string(b) != body {
			t.Errorf("Open changed a file that is %s", name)
		}
	}
}

// The file is private: an owner's data can name what a person asked the
// agent to watch.
func TestTheFileIsPrivate(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "state", "timers.json")
	s := open(t, path, newFakeClock(t0))
	mustSet(t, s, Timer{Owner: "a", ID: "x", Schedule: Schedule{At: t0.Add(time.Hour)}})
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("the timers file has mode %o, want no access for group or others", perm)
	}
}

// 🚨 The service is harness code with no model in it. It must not import the
// agent core or a provider, so that a timer can never start a model call by
// itself.
func TestThePackageImportsNoCoreAndNoProvider(t *testing.T) {
	pkgs, err := parser.ParseDir(token.NewFileSet(), ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			checked++
			for _, imp := range f.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if strings.HasPrefix(path, "terva.sh/terva/packages/core") || strings.HasPrefix(path, "terva.sh/terva/packages/provider") {
					t.Errorf("%s imports %s", name, path)
				}
			}
		}
	}
	if checked < 3 {
		t.Fatalf("the check read %d source files, so it did not see the package", checked)
	}
}
