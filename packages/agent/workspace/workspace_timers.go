package workspace

// The daemon's timer service (decision 0029 rule 3). The workspace owns one
// packages/agent/timers service: it opens the file at start, stops the loop at
// close, and shows the timers to a person. It sets no timer of its own. A wake
// and a routine are the owners, and each registers through Timers.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/timers"
	"terva.sh/terva/packages/filelock"
	"terva.sh/terva/packages/i18n"
)

// TimersDirName is the directory under $TERVA_HOME that holds each daemon's
// timers.
const TimersDirName = "timers"

// wsTimers is the workspace's hold on the timer service.
type wsTimers struct {
	mu sync.Mutex
	// svc is nil in a host that runs no timers, and in a daemon whose service
	// did not open. problem then says why, and it is empty in the first case.
	svc     *timers.Service
	lock    *filelock.Lock
	problem string
	// closed is set when the daemon shuts down. No service opens after it,
	// because nothing would stop that service or let go of its lock.
	closed bool
	// sayMu makes one timersChanged run at a time. The service reports
	// changes from more than one goroutine, and a report that read the list
	// early must not write its answer after a report that read it late.
	sayMu sync.Mutex
	// announced is whether clients were last told that the pane exists. Only
	// timersChanged writes it. A read of the pane list must not, or the list
	// one client reads between a change and its announcement would keep the
	// announcement from every other client.
	announced bool
	// clock is the service's time source. A test sets it before LoadTimers.
	clock timers.Clock
}

// timersDir is where the daemon in cwd keeps its timers. Two daemons can share
// one $TERVA_HOME from different directories, and each has timers of its own,
// so the directory is in the path. The same rule keys a talkoot's drafts.
func timersDir(root, cwd string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(cwd)))
	return filepath.Join(root, TimersDirName, hex.EncodeToString(sum[:8]))
}

// LoadTimers opens the daemon's timers and starts the service. A daemon calls
// it once at start. An in-process terminal does not: it ends with its window,
// and a timer that fires only while a window is open is not one a person can
// rely on.
//
// 🚨 Two daemons in one directory must not both fire the same timer. The
// first one holds a lock for as long as it runs. The second one reports that
// in the list and fires nothing.
//
// A failure here does not stop the daemon. The list carries the reason, and
// the rest of the daemon works without timers.
func (w *Workspace) LoadTimers() {
	// ⚠️ After the unlock. A timer that is set reports the change, and that
	// report takes the mutex which loadTimers holds.
	w.syncRoutineTimers(w.loadTimers())
}

// loadTimers is LoadTimers under the mutex. It returns the service that it
// started, or nil when it started none.
func (w *Workspace) loadTimers() *timers.Service {
	w.timers.mu.Lock()
	defer w.timers.mu.Unlock()
	if w.timers.closed || w.timers.svc != nil || w.timers.lock != nil {
		return nil
	}
	fail := func(why string) {
		w.timers.problem = why
		// The pane exists from the start, with the reason in it.
		w.timers.announced = true
		// The package's own errors already name it.
		w.diagf("timers: %s", strings.TrimPrefix(why, "timers: "))
	}
	dir := timersDir(w.root, w.cwd)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fail(i18n.T("the timers directory does not open: %v", err))
		return nil
	}
	lock, ok, err := filelock.TryAcquire(filepath.Join(dir, ".lock"))
	if err != nil {
		fail(i18n.T("the timers lock does not open: %v", err))
		return nil
	}
	if !ok {
		fail(i18n.T("another terva daemon in this directory runs the timers"))
		return nil
	}
	svc, err := timers.Open(filepath.Join(dir, "timers.json"), timers.Options{
		Clock:    w.timers.clock,
		Logf:     w.diagf,
		OnChange: w.timersChanged,
	})
	if err != nil {
		lock.Release()
		fail(err.Error())
		return nil
	}
	w.timers.svc, w.timers.lock, w.timers.problem = svc, lock, ""
	// Before Start, so a wake that came due while the daemon was down finds
	// its handler. It registers only once the talkoots run.
	w.attachWakeTimers(svc)
	w.attachRoutineTimers(svc)
	// What a client that lists the panes now is told. A daemon that starts
	// with timers in its file offers the pane at once.
	w.timers.announced = len(svc.List()) > 0
	svc.Start(w.ctx)
	return svc
}

// Timers returns the daemon's timer service, or nil in a host that runs none.
// An owner registers its handler here and sets its timers.
func (w *Workspace) Timers() *timers.Service {
	w.timers.mu.Lock()
	defer w.timers.mu.Unlock()
	return w.timers.svc
}

// stopTimers ends the loop and lets go of the lock. Close calls it before it
// stops what a firing would reach, so that no timer fires into a daemon that
// is shutting down.
func (w *Workspace) stopTimers() {
	w.timers.mu.Lock()
	svc, lock := w.timers.svc, w.timers.lock
	w.timers.svc, w.timers.lock, w.timers.closed = nil, nil, true
	w.timers.mu.Unlock()
	if svc != nil {
		// Outside the mutex: Stop waits for a handler, and a handler may call
		// Timers.
		svc.Stop()
	}
	lock.Release()
}

// timersChanged tells every client that the timers pane has new content. It
// runs on the service's goroutine, and a broadcast does not block.
func (w *Workspace) timersChanged() {
	w.timers.sayMu.Lock()
	defer w.timers.sayMu.Unlock()
	shown := w.timersShown()
	w.timers.mu.Lock()
	flipped := shown != w.timers.announced
	w.timers.announced = shown
	w.timers.mu.Unlock()
	if flipped {
		w.BroadcastAll(ctrlproto.SurfacesChangedEvent())
	}
	w.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("timers"))
}

// timersShown reports whether the timers pane has something to say: a timer,
// or the reason the service does not run. A daemon with neither offers no
// pane, because most daemons hold no timer.
func (w *Workspace) timersShown() bool {
	w.timers.mu.Lock()
	svc, problem := w.timers.svc, w.timers.problem
	w.timers.mu.Unlock()
	if svc == nil {
		return problem != ""
	}
	return svc.Problem() != "" || len(svc.List()) > 0
}

// TimerList serves timers.list. It implements ctrlproto.TimersController.
func (w *Workspace) TimerList(_ context.Context) (ctrlproto.TimersResult, error) {
	w.timers.mu.Lock()
	svc, problem := w.timers.svc, w.timers.problem
	w.timers.mu.Unlock()
	if svc == nil {
		if problem == "" {
			return ctrlproto.TimersResult{}, ctrlproto.Errorf(ctrlproto.CodeUnsupported, "%s", i18n.T("this host runs no timer service"))
		}
		return ctrlproto.TimersResult{Timers: []ctrlproto.TimerInfo{}, Problem: problem}, nil
	}
	list := svc.List()
	out := ctrlproto.TimersResult{Timers: make([]ctrlproto.TimerInfo, 0, len(list)), Problem: svc.Problem()}
	for _, t := range list {
		out.Timers = append(out.Timers, ctrlproto.TimerInfo{
			Owner: t.Owner, ID: t.ID, Label: t.Label,
			Schedule: t.Schedule.String(), Recurs: t.Schedule.Recurs(),
			Next: t.Next, LastFired: t.LastFired, Problem: t.Problem,
		})
	}
	return out, nil
}

// timerStamp is how the pane writes a time: to the minute, in the daemon's
// zone, with the zone named.
const timerStamp = "2006-01-02 15:04 MST"

// timersPanel renders the timers pane. It is the same data as timers.list, as
// lines a person reads.
func (w *Workspace) timersPanel() *ctrlproto.PanelView {
	res, err := w.TimerList(context.Background())
	if err != nil {
		return &ctrlproto.PanelView{Lines: []string{}, Footer: err.Error()}
	}
	lines := make([]string, 0, len(res.Timers))
	for _, t := range res.Timers {
		name := t.Owner + "/" + t.ID
		if t.Label != "" {
			name = i18n.T("%s (%s)", t.Label, name)
		}
		var line string
		switch {
		case t.Problem != "":
			line = i18n.T("%s: %s, waits: %s", name, t.Schedule, t.Problem)
		case t.Next.IsZero():
			line = i18n.T("%s: %s, fires now", name, t.Schedule)
		default:
			line = i18n.T("%s: %s, next %s", name, t.Schedule, t.Next.In(time.Local).Format(timerStamp))
		}
		lines = append(lines, line)
	}
	footer := res.Problem
	if footer == "" {
		footer = i18n.T("A timer fires once for the latest window. It does not replay the windows a stopped daemon missed.")
	}
	return &ctrlproto.PanelView{Lines: lines, Footer: footer}
}
