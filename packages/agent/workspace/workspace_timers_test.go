package workspace

// The daemon's hold on the timer service. Each test runs a real workspace and
// a real service over a clock the test moves. No test waits on the wall clock
// for a timer to come due.

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/timers"
	"terva.sh/terva/packages/testsupport"
)

// stepClock is a clock that only the test moves. After never fires, so the
// service's loop wakes only when a timer is set, and the test calls Poll after
// it moves the time.
type stepClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) After(time.Duration) <-chan time.Time { return nil }

func (c *stepClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

var timersT0 = time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)

// timersHome points $TERVA_HOME at a fresh directory and returns a directory
// for a daemon to run in.
func timersHome(t *testing.T) string {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	return testsupport.TempDir(t)
}

// openTimerDaemon builds a workspace in cwd over clock and starts its timers,
// the way terva web does.
func openTimerDaemon(t *testing.T, cwd string, clock timers.Clock) *Workspace {
	t.Helper()
	w := openTalkootWorkspace(t, cwd)
	w.timers.clock = clock
	w.LoadTimers()
	return w
}

// firings collects what one owner received.
type firings struct{ ch chan timers.Firing }

func newFirings() *firings { return &firings{ch: make(chan timers.Firing, 8)} }

func (f *firings) handle(got timers.Firing) { f.ch <- got }

func (f *firings) next(t *testing.T, what string) timers.Firing {
	t.Helper()
	select {
	case got := <-f.ch:
		return got
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return timers.Firing{}
	}
}

func (f *firings) none(t *testing.T, what string) {
	t.Helper()
	select {
	case got := <-f.ch:
		t.Fatalf("%s: got the firing %+v", what, got)
	default:
	}
}

func TestADaemonKeepsATimerAcrossARestart(t *testing.T) {
	cwd := timersHome(t)
	clock := &stepClock{now: timersT0}
	w := openTimerDaemon(t, cwd, clock)
	if w.Timers() == nil {
		t.Fatalf("the daemon runs no timer service: %q", w.timers.problem)
	}
	if _, err := w.Timers().Set(timers.Timer{
		Owner: "wake", ID: "ci", Label: "Look at the CI run",
		Schedule: timers.Schedule{At: timersT0.Add(20 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	if w.Timers() != nil {
		t.Fatal("a closed daemon still hands out its timer service")
	}
	// A late start must not open a service that nothing will stop. It would
	// hold the directory's lock, and the next daemon would fire no timer.
	w.LoadTimers()
	if w.Timers() != nil {
		t.Fatal("a closed daemon opened its timers again")
	}

	// The daemon was down for an hour, and the timer's time passed.
	clock.advance(time.Hour)
	w2 := openTimerDaemon(t, cwd, clock)
	got := newFirings()
	w2.Timers().Handle("wake", got.handle)
	f := got.next(t, "the timer to fire after the restart")
	if f.Timer.ID != "ci" || f.Late != 40*time.Minute {
		t.Fatalf("firing = %+v, want the ci timer, 40 minutes late", f)
	}
	w2.Timers().Poll()
	got.none(t, "a one-time timer fires once")
}

func TestTimersListNamesEachTimerAndItsNextWindow(t *testing.T) {
	cwd := timersHome(t)
	clock := &stepClock{now: timersT0}
	w := openTimerDaemon(t, cwd, clock)
	ctx := t.Context()
	got := newFirings()
	w.Timers().Handle("routine", got.handle)

	for _, tm := range []timers.Timer{
		{Owner: "routine", ID: "brief", Label: "Morning brief", Schedule: timers.Schedule{Every: timers.Duration(time.Hour), Anchor: timersT0}},
		{Owner: "wake", ID: "ci", Schedule: timers.Schedule{At: timersT0.Add(20 * time.Minute)}},
	} {
		if _, err := w.Timers().Set(tm); err != nil {
			t.Fatal(err)
		}
	}

	res, err := w.TimerList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Problem != "" || len(res.Timers) != 2 {
		t.Fatalf("list = %+v, want two timers and no problem", res)
	}
	// The soonest is first.
	ci, brief := res.Timers[0], res.Timers[1]
	if ci.Owner != "wake" || ci.ID != "ci" || ci.Recurs || !ci.Next.Equal(timersT0.Add(20*time.Minute)) {
		t.Errorf("first timer = %+v, want the one-time ci wake at 09:20", ci)
	}
	if ci.Schedule != "at 2026-11-17T09:20:00Z" {
		t.Errorf("ci schedule = %q", ci.Schedule)
	}
	// Nothing in this daemon handles a wake, and the list says so.
	if !strings.Contains(ci.Problem, `"wake"`) {
		t.Errorf("ci problem = %q, want it to name the owner with no handler", ci.Problem)
	}
	if brief.Label != "Morning brief" || brief.Schedule != "every 1h" || !brief.Recurs ||
		!brief.Next.Equal(timersT0.Add(time.Hour)) || brief.Problem != "" || !brief.LastFired.IsZero() {
		t.Errorf("second timer = %+v, want the hourly brief at 10:00, in order", brief)
	}

	// A firing moves the next window, and the list follows.
	clock.advance(time.Hour)
	w.Timers().Poll()
	got.next(t, "the brief to fire")
	res, err = w.TimerList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tm := range res.Timers {
		if tm.ID != "brief" {
			continue
		}
		if !tm.Next.Equal(timersT0.Add(2*time.Hour)) || !tm.LastFired.Equal(timersT0.Add(time.Hour)) {
			t.Errorf("after a firing the brief = %+v, want next 11:00 and last fired 10:00", tm)
		}
	}
}

// timersMeta returns the timers pane's entry in the session's list of panes.
func timersMeta(t *testing.T, w *Workspace, sess string) *ctrlproto.SurfaceMeta {
	t.Helper()
	metas, err := w.Surfaces(t.Context(), sess)
	if err != nil {
		t.Fatal(err)
	}
	for i := range metas {
		if metas[i].ID == "timers" {
			return &metas[i]
		}
	}
	return nil
}

func TestTheTimersPaneShowsWhatTheDaemonWillDo(t *testing.T) {
	cwd := timersHome(t)
	clock := &stepClock{now: timersT0}
	w := openTimerDaemon(t, cwd, clock)
	ctx := t.Context()
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if meta := timersMeta(t, w, info.ID); meta != nil {
		t.Fatalf("a daemon with no timer offers the pane: %+v", *meta)
	}

	evCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events, err := w.Subscribe(evCtx, ctrlproto.AddrWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	w.Timers().Handle("routine", func(timers.Firing) {})
	if _, err := w.Timers().Set(timers.Timer{
		Owner: "routine", ID: "brief", Label: "Morning brief",
		Schedule: timers.Schedule{Every: timers.Duration(time.Hour), Anchor: timersT0},
	}); err != nil {
		t.Fatal(err)
	}

	// The first timer changes the list of panes, and every later change
	// changes the pane's content. A client hears both.
	var listed, updated bool
	deadline := time.After(10 * time.Second)
	for !listed || !updated {
		select {
		case ev := <-events:
			switch {
			case ev.Type == ctrlproto.EventSurfacesChanged:
				listed = true
			case ev.Type == ctrlproto.EventSurfaceUpdated && ev.SurfaceID == "timers":
				updated = true
			}
		case <-deadline:
			t.Fatalf("the new timer was not announced: panes listed again %v, pane updated %v", listed, updated)
		}
	}

	meta := timersMeta(t, w, info.ID)
	if meta == nil {
		t.Fatal("a daemon with a timer does not offer the pane")
	}
	if meta.Kind != "panel" || meta.Scope != "workspace" || !meta.Live || meta.Actions {
		t.Errorf("timers meta = %+v, want a live workspace panel with no actions", *meta)
	}
	surf, err := w.Surface(ctx, info.ID, "timers")
	if err != nil {
		t.Fatal(err)
	}
	if surf.Kind != "panel" || surf.Panel == nil || len(surf.Panel.Lines) != 1 {
		t.Fatalf("timers pane = %+v, want one line", surf)
	}
	want := "Morning brief (routine/brief): every 1h, next " + timersT0.Add(time.Hour).In(time.Local).Format(timerStamp)
	if surf.Panel.Lines[0] != want {
		t.Errorf("line = %q, want %q", surf.Panel.Lines[0], want)
	}
	// A pane with no extension behind it takes no keys.
	if surf.Panel.Ext != "" {
		t.Errorf("the timers pane names an extension: %q", surf.Panel.Ext)
	}

	if _, err := w.Timers().Cancel("routine", "brief"); err != nil {
		t.Fatal(err)
	}
	if meta := timersMeta(t, w, info.ID); meta != nil {
		t.Fatalf("the pane stays after the last timer went: %+v", *meta)
	}
}

// 🚨 Two daemons that both fired one file's timers would run every wake
// twice. The second daemon must hold no service, and it must say why.
func TestASecondDaemonInOneDirectoryFiresNoTimer(t *testing.T) {
	cwd := timersHome(t)
	clock := &stepClock{now: timersT0}
	first := openTimerDaemon(t, cwd, clock)
	if _, err := first.Timers().Set(timers.Timer{
		Owner: "wake", ID: "ci", Schedule: timers.Schedule{At: timersT0.Add(time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}

	second := openTimerDaemon(t, cwd, clock)
	if second.Timers() != nil {
		t.Fatal("a second daemon in the same directory opened the timers")
	}
	res, err := second.TimerList(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Timers) != 0 || !strings.Contains(res.Problem, "another terva daemon") {
		t.Fatalf("the second daemon's list = %+v, want no timers and the reason", res)
	}
	info, err := second.CreateSession(t.Context(), ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if timersMeta(t, second, info.ID) == nil {
		t.Fatal("the second daemon hides the reason it runs no timers")
	}

	// A daemon in another directory is not the same daemon.
	elsewhere := openTimerDaemon(t, testsupport.TempDir(t), clock)
	if elsewhere.Timers() == nil {
		t.Fatalf("a daemon in another directory got no timers: %q", elsewhere.timers.problem)
	}
	if got := elsewhere.Timers().List(); len(got) != 0 {
		t.Fatalf("a daemon in another directory sees this one's timers: %+v", got)
	}

	// The lock goes with the daemon that held it.
	_ = first.Close()
	_ = second.Close()
	third := openTimerDaemon(t, cwd, clock)
	if third.Timers() == nil {
		t.Fatalf("the directory stayed locked after its daemon closed: %q", third.timers.problem)
	}
	if _, ok := third.Timers().Get("wake", "ci"); !ok {
		t.Fatal("the next daemon lost the timer")
	}
}

// A handler reaches the rest of the daemon: a talkoot, a session, the swarm.
// Close must end the timers before it stops any of them, and it must wait for
// a firing that is in flight.
func TestCloseStopsTheTimersBeforeWhatAFiringReaches(t *testing.T) {
	cwd := timersHome(t)
	clock := &stepClock{now: timersT0}
	w := openTimerDaemon(t, cwd, clock)
	if _, err := w.CreateSession(t.Context(), ctrlproto.CreateOpts{}); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	w.Timers().Handle("wake", func(timers.Firing) {
		close(entered)
		<-release
	})
	if _, err := w.Timers().Set(timers.Timer{
		Owner: "wake", ID: "ci", Schedule: timers.Schedule{At: timersT0},
	}); err != nil {
		t.Fatal(err)
	}
	<-entered

	closed := make(chan struct{})
	go func() { defer close(closed); _ = w.Close() }()
	waitFor(t, "Close to reach the timers", func() bool {
		w.timers.mu.Lock()
		defer w.timers.mu.Unlock()
		return w.timers.closed
	})
	// Close waits here for the handler. Nothing the handler could reach has
	// stopped yet.
	w.talkoot.mu.Lock()
	stopping := w.talkoot.stopping
	w.talkoot.mu.Unlock()
	w.mu.Lock()
	sessions := len(w.sessions)
	w.mu.Unlock()
	if stopping || sessions != 1 || w.ctx.Err() != nil {
		t.Fatalf("while a firing runs: talkoots stopping %v, %d sessions, context %v; want none of it stopped", stopping, sessions, w.ctx.Err())
	}
	select {
	case <-closed:
		t.Fatal("Close returned while a handler still ran")
	default:
	}
	close(release)
	<-closed
}

// One client that lists the panes between a change and its announcement must
// not keep the announcement from the other clients.
func TestAPaneListDoesNotSwallowTheAnnouncement(t *testing.T) {
	cwd := timersHome(t)
	w := openTimerDaemon(t, cwd, &stepClock{now: timersT0})
	ctx := t.Context()
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	evCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events, err := w.Subscribe(evCtx, ctrlproto.AddrWorkspace)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := w.Timers().Set(timers.Timer{
		Owner: "wake", ID: "a", Schedule: timers.Schedule{At: timersT0.Add(time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	// Put the service back to the moment between the change and its
	// announcement: the timer is in the list, and no client was told.
	w.timers.mu.Lock()
	w.timers.announced = false
	w.timers.mu.Unlock()
	for len(events) > 0 {
		<-events
	}
	// One client lists the panes at that moment, and sees the new one.
	if timersMeta(t, w, info.ID) == nil {
		t.Fatal("the pane is not offered while the timer exists")
	}
	w.timersChanged()

	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Type == ctrlproto.EventSurfacesChanged {
				return
			}
		case <-deadline:
			t.Fatal("no client was told to list the panes again")
		}
	}
}

// A client can ask for a pane by name without the list. A session with no
// workspace behind it has no timers pane, and must say so and not panic.
func TestASessionWithNoWorkspaceHasNoTimersPane(t *testing.T) {
	_, err := (&wsSession{}).surface("timers")
	var ce *ctrlproto.Error
	if !errors.As(err, &ce) || ce.Code != ctrlproto.CodeNotFound {
		t.Fatalf("surface(timers) with no workspace = %v, want not found", err)
	}
}

func TestAHostWithNoTimerServiceSaysSo(t *testing.T) {
	cwd := timersHome(t)
	w := openTalkootWorkspace(t, cwd)
	_, err := w.TimerList(t.Context())
	var ce *ctrlproto.Error
	if !errors.As(err, &ce) || ce.Code != ctrlproto.CodeUnsupported {
		t.Fatalf("TimerList on a host with no timers = %v, want unsupported", err)
	}
	info, err := w.CreateSession(t.Context(), ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if meta := timersMeta(t, w, info.ID); meta != nil {
		t.Fatalf("a host with no timer service offers the pane: %+v", *meta)
	}
}

// A file that does not read must not stop the daemon, and must not be
// replaced: it may hold timers that a repair can recover.
func TestADamagedTimersFileLeavesTheDaemonUp(t *testing.T) {
	cwd := timersHome(t)
	dir := timersDir(os.Getenv("TERVA_HOME"), cwd)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := dir + string(os.PathSeparator) + "timers.json"
	if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	w := openTimerDaemon(t, cwd, &stepClock{now: timersT0})
	if w.Timers() != nil {
		t.Fatal("the daemon opened a timers file that does not read")
	}
	res, err := w.TimerList(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if res.Problem == "" {
		t.Fatal("the list does not say why the daemon runs no timers")
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "{ not json" {
		t.Fatalf("the damaged file changed: %q, %v", body, err)
	}
	// The lock went back, so a daemon that starts after the repair is not
	// refused.
	_ = w.Close()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if again := openTimerDaemon(t, cwd, &stepClock{now: timersT0}); again.Timers() == nil {
		t.Fatalf("the daemon after the repair got no timers: %q", again.timers.problem)
	}
}
