package workspace

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modelreg"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/filelock"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/session"
)

// Errors from the talkoot API.
var (
	ErrTalkootNotFound = errors.New("talkoot: no such talkoot")
	ErrTalkootNotHere  = errors.New("talkoot: this workspace does not run that talkoot")
	ErrTalkootClosed   = errors.New("talkoot: the talkoot has stopped")
	ErrTalkootExists   = errors.New("talkoot: that id is taken")
)

// talkootRunLock is held by the one process that runs a talkoot.
//
// 🚨 Two routers over one room would each seal to their own last line, and
// every line the other wrote would then fail its seal. The daemon and an
// in-process terminal can share a directory, so the lock is not optional.
const talkootRunLock = "run.lock"

// talkootRun is one talkoot this workspace runs.
type talkootRun struct {
	id, dir string
	// mu is held for reading around every router call, and for writing to
	// swap the router or to close the run.
	//
	// ⚠️ Nothing inside a router call may take mu again. A second read lock
	// blocks behind a waiting writer, and the writer waits for the first.
	mu     sync.RWMutex
	router *talkoot.Router
	// update serializes roster updates, so each one diffs against the roster
	// it replaces. It is taken before mu.
	update sync.Mutex
	// propose holds one proposal from its count of the pending ones to its
	// save, so concurrent proposers cannot pass the cap together. It is
	// taken before mu, and nothing that holds update or mu waits for it.
	propose sync.Mutex
	closed  bool
	// stopping is set when shutdown stops the router. An update then refuses,
	// so it cannot swap in a router that was never stopped.
	stopping bool
	// roster is read without mu, by a session build inside a delivery.
	roster atomic.Pointer[talkoot.Roster]
	room   *talkoot.Room
	lock   *filelock.Lock
	// seats maps a member to its session id. Guarded by wsTalkoot.mu.
	seats map[string]string
	// open counts each member's turns that have not reported, by member id,
	// whether or not the member is still on the roster. Guarded by
	// wsTalkoot.mu, which is taken after mu.
	open map[string]int

	emit func(talkootEvent)
	// flushMu holds one flush from taking its lines to sending them, so a
	// watcher sees events in the room's order.
	flushMu sync.Mutex
	evMu    sync.Mutex
	pending []talkoot.Line
	last    map[string]talkoot.Status
}

// talkootEvent is a change a client of the talkoot wants to see.
type talkootEvent struct {
	Talkoot string
	// Kind is envelope, status, roster, inbox, or loaded.
	Kind   string
	Line   *talkoot.Line
	Status []talkoot.Status
	// Wire is an inbox event, built in its wire form, because an inbox
	// card is made of wire requests already.
	Wire *ctrlproto.Event
}

// do runs fn against the router, then sends the events it caused.
func (r *talkootRun) do(fn func(*talkoot.Router) error) error {
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return ErrTalkootClosed
	}
	err := fn(r.router)
	r.mu.RUnlock()
	r.flush()
	return err
}

// observe queues each sealed line. It runs with the room locked, so it only
// queues; flush sends.
func (r *talkootRun) observe(l talkoot.Line) {
	r.evMu.Lock()
	r.pending = append(r.pending, l)
	r.evMu.Unlock()
}

// flush sends the envelope, answer, and roster lines queued since the last
// flush, and the member statuses that changed.
func (r *talkootRun) flush() {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	r.evMu.Lock()
	lines := r.pending
	r.pending = nil
	r.evMu.Unlock()
	for i := range lines {
		switch lines[i].Type {
		case talkoot.LineEnvelope:
			r.emit(talkootEvent{Talkoot: r.id, Kind: "envelope", Line: &lines[i]})
		case talkoot.LineAnswer:
			r.emit(talkootEvent{Talkoot: r.id, Kind: "answer", Line: &lines[i]})
		case talkoot.LineRoster:
			r.emit(talkootEvent{Talkoot: r.id, Kind: "roster", Line: &lines[i]})
		}
	}
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return
	}
	st := r.router.Statuses()
	r.mu.RUnlock()
	r.evMu.Lock()
	changed := len(st) != len(r.last)
	next := make(map[string]talkoot.Status, len(st))
	for _, s := range st {
		next[s.Member] = s
		if r.last[s.Member] != s {
			changed = true
		}
	}
	r.last = next
	r.evMu.Unlock()
	if changed {
		r.emit(talkootEvent{Talkoot: r.id, Kind: "status", Status: st})
	}
}

// LoadTalkoots starts every talkoot homed in this workspace's directory. The
// daemon hosts call it once after NewWorkspace. The in-process terminal does
// not, so it never takes a talkoot away from a daemon in the same directory.
func (w *Workspace) LoadTalkoots() {
	if !config.TalkootEnabled() {
		return
	}
	ids, err := talkoot.List()
	if err != nil {
		w.diagf("talkoot: %v", err)
		return
	}
	for _, id := range ids {
		if err := w.startTalkoot(id); err != nil && !errors.Is(err, errNotHomedHere) {
			w.talkootProblem(id, err.Error())
			w.diagf("talkoot %s did not start: %v", id, err)
		}
	}
}

var errNotHomedHere = errors.New("talkoot: homed in another directory")

func (w *Workspace) talkootProblem(id, why string) {
	w.talkoot.mu.Lock()
	defer w.talkoot.mu.Unlock()
	if w.talkoot.problems == nil {
		w.talkoot.problems = map[string]string{}
	}
	if why == "" {
		delete(w.talkoot.problems, id)
		return
	}
	w.talkoot.problems[id] = why
}

// sameDir reports whether two paths name the same directory.
func sameDir(a, b string) bool {
	norm := func(p string) string {
		if rest, ok := strings.CutPrefix(p, "~"); ok {
			if home, err := os.UserHomeDir(); err == nil {
				p = home + rest
			}
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		return filepath.Clean(p)
	}
	return norm(a) == norm(b)
}

// startTalkoot loads, locks, and starts one talkoot, then releases what the
// room still owes.
func (w *Workspace) startTalkoot(id string) error {
	r, err := talkoot.Load(id, talkootEnv())
	if err != nil {
		return err
	}
	if !sameDir(r.Home, w.cwd) {
		return errNotHomedHere
	}
	dir := filepath.Join(talkoot.Dir(), id)
	lk, ok, err := filelock.TryAcquire(filepath.Join(dir, talkootRunLock))
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("another terva process runs this talkoot")
	}
	// A talkoot made before the notes existed gets its directory here.
	if err := privfs.MkdirAll(filepath.Join(dir, talkoot.NotesDir)); err != nil {
		lk.Release()
		return err
	}
	run := &talkootRun{id: id, dir: dir, room: talkoot.OpenRoom(dir), lock: lk, seats: map[string]string{}}
	run.roster.Store(&r)
	run.emit = w.talkootEmit
	lines, err := run.room.Read()
	if err != nil {
		lk.Release()
		return err
	}
	for _, l := range lines {
		if l.Type != talkoot.LineSeat {
			continue
		}
		if l.Ref == "" {
			delete(run.seats, l.Member)
		} else {
			run.seats[l.Member] = l.Ref
		}
	}
	run.room.Observe(run.observe)
	rt, err := talkoot.NewRouter(r, run.room, w.talkootDriversFor(run), talkoot.DefaultLimits(), nil)
	if err != nil {
		lk.Release()
		return err
	}
	run.router = rt

	w.talkoot.mu.Lock()
	if w.talkoot.stopping {
		w.talkoot.mu.Unlock()
		lk.Release()
		return ErrTalkootClosed
	}
	if w.talkoot.runs == nil {
		w.talkoot.runs = map[string]*talkootRun{}
	}
	if w.talkoot.seats == nil {
		w.talkoot.seats = map[string]*seatBinding{}
	}
	w.talkoot.runs[id] = run
	delete(w.talkoot.problems, id)
	for _, l := range lines {
		if l.Type == talkoot.LineSeat && l.Ref != "" {
			w.talkoot.markMemberLocked(l.Ref)
		}
	}
	// A seat is bound without waking its session. The session materializes on
	// its next delivery, or when a person opens it, and its build finds the
	// seat.
	for member, sid := range run.seats {
		m, ok := memberOf(r, member)
		if !ok || m.Driver != talkoot.DriverNative {
			continue
		}
		if _, err := os.Stat(w.sessionPath(sid)); err == nil {
			w.talkoot.seats[sid] = &seatBinding{run: run, member: member}
		}
	}
	w.talkoot.mu.Unlock()

	w.talkootEmit(talkootEvent{Talkoot: id, Kind: "loaded"})
	w.BroadcastAll(ctrlproto.TalkootsChangedEvent())
	return run.do(func(rt *talkoot.Router) error {
		rt.Release()
		return nil
	})
}

func memberOf(r talkoot.Roster, id string) (talkoot.Member, bool) {
	for _, m := range r.Members {
		if m.ID == id {
			return m, true
		}
	}
	return talkoot.Member{}, false
}

// talkootDriversFor binds a run's members: a native member to its session,
// made on its first delivery; a worker member to nothing yet. A native
// member's reads reach the run's router through the member's seat.
func (w *Workspace) talkootDriversFor(run *talkootRun) talkoot.Drivers {
	return w.talkootDrivers(
		func(_ string, m talkoot.Member) (string, error) { return w.memberSession(run, m) },
		func(_ string, m talkoot.Member) (string, error) {
			return "", errors.New("worker members are not bound to an agent yet")
		},
		func(sessID, member string, r talkoot.Receipt) { w.talkootRead(sessID, run, member, r) },
	)
}

// memberSession returns the session a native member runs in, and makes one
// when it has none. It runs inside a delivery.
//
// 🔑 A member's session is made on its first delivery, not when the talkoot
// starts. An empty session deletes itself on close and at the next start, so a
// session made early could vanish before it ever ran. When one does, the next
// delivery makes another, and nothing is lost: it held nothing.
func (w *Workspace) memberSession(run *talkootRun, m talkoot.Member) (string, error) {
	if m.Workspace == talkoot.WorkspaceWorktree {
		return "", errors.New("native members cannot run in a worktree yet")
	}
	w.talkoot.mu.Lock()
	id := run.seats[m.ID]
	w.talkoot.mu.Unlock()
	if id != "" {
		if _, err := os.Stat(w.sessionPath(id)); err == nil {
			return id, nil
		}
	}
	w.mu.Lock()
	s, old, err := w.createMemberLocked(run, m)
	w.mu.Unlock()
	// Outside w.mu: a revoke waits for a call in flight on the old seat, and
	// that call may be waiting for w.mu.
	old.revoke()
	if err != nil {
		return "", err
	}
	w.BroadcastAll(ctrlproto.SessionsChangedEvent())
	return s.id, nil
}

// createMemberLocked makes a native member's session. It seats the session
// before building it, so the build sees the member's posture and tools. It
// returns the seat it replaced, for the caller to revoke outside w.mu.
func (w *Workspace) createMemberLocked(run *talkootRun, m talkoot.Member) (*wsSession, *seatBinding, error) {
	prov, model, _ := w.effectiveDefaultModel("", "")
	reasoning := ""
	switch {
	case m.Model != "":
		found, err := modelreg.FindModel("", m.Model)
		if err != nil {
			return nil, nil, fmt.Errorf("model %q: %w", m.Model, err)
		}
		prov, model = found.Provider, found.ID
	case m.Tier != "":
		cfg, _ := config.LoadConfig()
		if pick := tools.ResolveSwarmTier(prov, model, m.Tier, build.SwarmTierMap(cfg.SwarmTiers)); pick.Model != "" {
			model, reasoning = pick.Model, pick.Reasoning
		}
	}
	sess, err := session.NewSession(w.root, w.cwd, prov, model, w.version)
	if err != nil {
		return nil, nil, fmt.Errorf("create session: %w", err)
	}
	fail := func(err error) (*wsSession, *seatBinding, error) {
		_ = sess.Close()
		_ = os.Remove(sess.Path)
		return nil, nil, err
	}
	title := run.id + " · " + m.ID
	_ = session.RenameSession(sess.Path, title)
	sess.Meta.Title = title
	if err := sess.SetCreationSpec(m.Persona, "", "", nil, 0); err != nil {
		return fail(fmt.Errorf("persist session spec: %w", err))
	}
	if reasoning != "" {
		if err := sess.UpdateReasoning(reasoning); err != nil {
			return fail(fmt.Errorf("persist reasoning: %w", err))
		}
	}
	id := build.SessionIDFromPath(sess.Path)
	// 🚨 The seat line comes before the seat. It binds the member again after
	// a restart, and it keeps the session out of the person's default. A
	// session that the room does not record never runs as a member.
	if err := run.room.Append(talkoot.Line{Type: talkoot.LineSeat, At: time.Now(), Member: m.ID, Ref: id}); err != nil {
		return fail(fmt.Errorf("the room could not record the seat: %w", err))
	}

	w.talkoot.mu.Lock()
	prev := run.seats[m.ID]
	old := w.talkoot.seats[prev]
	delete(w.talkoot.seats, prev)
	run.seats[m.ID] = id
	w.talkoot.seats[id] = &seatBinding{run: run, member: m.ID}
	w.talkoot.markMemberLocked(id)
	w.talkoot.mu.Unlock()

	s, err := w.buildSession(id, sess, nil, build.ResumeWindow{})
	if err != nil {
		// Put the previous seat back as it was: nothing replaced it.
		w.talkoot.mu.Lock()
		delete(w.talkoot.seats, id)
		run.seats[m.ID] = prev
		if old != nil {
			w.talkoot.seats[prev] = old
		}
		w.talkoot.mu.Unlock()
		return fail(err)
	}
	w.sessions[id] = s
	return s, old, nil
}

// talkootPostureOf returns the approval mode the roster gives a seated
// session, or "" for a session with no seat.
func (w *Workspace) talkootPostureOf(sessID string) string {
	w.talkoot.mu.Lock()
	b := w.talkoot.seats[sessID]
	w.talkoot.mu.Unlock()
	if b == nil {
		return ""
	}
	m, ok := memberOf(*b.run.roster.Load(), b.member)
	if !ok {
		return ""
	}
	return m.Posture
}

// narrowMemberTools removes from a seated member's registry every tool its
// roster tools list does not name. A session with no seat, or a member with
// no list, keeps every tool. The approval mode pruned inside Resolve first,
// so the result is the intersection, and the list never widens.
//
// 🚨 This runs after injectExtraTools, on the finished registry. Args.Tools
// filters inside Resolve and misses the extension, MCP, skill, and host
// tools, so a filter there would leave them all in place.
func (w *Workspace) narrowMemberTools(sessID string, r *build.Resolved) {
	list := w.talkootToolsOf(sessID)
	if list == nil {
		return
	}
	r.NarrowTools(memberToolFilter(list))
}

// memberToolFilter keeps a tool that list allows, by its name and its group.
func memberToolFilter(list []string) func(string, core.Tool) bool {
	return func(name string, t core.Tool) bool { return talkoot.MatchTool(list, name, core.ToolGroup(t)) }
}

// talkootToolsOf returns the tools list the roster gives a seated session,
// or nil for a session with no seat or a member with no list.
//
// 🚨 A seat whose member the roster no longer holds narrows to the seat tools
// alone. An update stores the new roster before it unseats a leaver, and a
// rebuild in that window must not hand the leaver the full set.
func (w *Workspace) talkootToolsOf(sessID string) []string {
	w.talkoot.mu.Lock()
	b := w.talkoot.seats[sessID]
	w.talkoot.mu.Unlock()
	if b == nil {
		return nil
	}
	m, ok := memberOf(*b.run.roster.Load(), b.member)
	if !ok {
		return []string{}
	}
	return m.Tools
}

// latestPersonSession is the newest session that no talkoot member ever held,
// for the default a client gets when it names no session. A member's session
// is not the person's conversation. It holds the team's envelopes, and after
// an unseat it keeps the member's posture until it is rebuilt.
//
// The first call also reads the seat lines of the talkoots homed here that
// another process runs, such as the daemon beside an in-process terminal.
//
// ⚠️ A member session that the other process makes after that first call can
// still be the default. The session lock keeps both processes from writing
// it at once.
func (w *Workspace) latestPersonSession() string {
	w.talkoot.foldOnce.Do(w.foldTalkootMembers)
	w.talkoot.mu.Lock()
	member := make(map[string]bool, len(w.talkoot.members))
	for id := range w.talkoot.members {
		member[id] = true
	}
	w.talkoot.mu.Unlock()
	for _, p := range session.ListSessions(w.root, w.cwd) {
		if !member[build.SessionIDFromPath(p)] {
			return p
		}
	}
	return ""
}

// foldTalkootMembers marks the member sessions of every talkoot homed here,
// from the room's seat lines. It reads and never writes, so it needs no run
// lock, and a talkoot another process runs is read too.
func (w *Workspace) foldTalkootMembers() {
	if !config.TalkootEnabled() {
		return
	}
	ids, err := talkoot.List()
	if err != nil {
		return
	}
	for _, id := range ids {
		r, err := talkoot.Load(id, talkootEnv())
		if err != nil || !sameDir(r.Home, w.cwd) {
			continue
		}
		lines, err := talkoot.OpenRoom(filepath.Join(talkoot.Dir(), id)).Read()
		if err != nil {
			w.diagf("talkoot %s: could not read the room for its member sessions: %v", id, err)
			continue
		}
		w.talkoot.mu.Lock()
		for _, l := range lines {
			if l.Type == talkoot.LineSeat && l.Ref != "" {
				w.talkoot.markMemberLocked(l.Ref)
			}
		}
		w.talkoot.mu.Unlock()
	}
}

// talkootEmit hands an event to every watcher of its talkoot.
func (w *Workspace) talkootEmit(ev talkootEvent) {
	w.talkoot.mu.Lock()
	fns := make([]func(talkootEvent), 0, len(w.talkoot.watchers[ev.Talkoot]))
	for _, fn := range w.talkoot.watchers[ev.Talkoot] {
		fns = append(fns, fn)
	}
	w.talkoot.mu.Unlock()
	for _, fn := range fns {
		fn(ev)
	}
}

// stopTalkoots holds every delivery before the sessions close, so a delivery
// lost to shutdown stays owed. Turns that end during shutdown still report.
//
// 🚨 Shutdown stops for good. No talkoot starts after it, and no update swaps
// in a router that was never stopped.
func (w *Workspace) stopTalkoots() {
	w.talkoot.mu.Lock()
	w.talkoot.stopping = true
	runs := make([]*talkootRun, 0, len(w.talkoot.runs))
	for _, r := range w.talkoot.runs {
		runs = append(runs, r)
	}
	w.talkoot.mu.Unlock()
	for _, r := range runs {
		r.mu.Lock()
		r.stopping = true
		if !r.closed {
			r.router.Stop()
		}
		r.mu.Unlock()
	}
}

// talkootCloseWait bounds how long shutdown waits for member turns to report.
var talkootCloseWait = 5 * time.Second

// closeTalkoots waits briefly for member turns to report their cost, then
// closes every run, revokes every seat, and lets go of the run locks.
//
// 🚨 A turn that has not reported by the deadline never will: a closed run
// refuses its report. Each such turn is recorded as free and its member
// pauses, so the spend caps do not forget it after a restart. The run's open
// count finds it, even for a member an update removed. Nothing writes to the
// room once a run is closed, so the lock is safe to release.
func (w *Workspace) closeTalkoots() {
	for deadline := time.Now().Add(talkootCloseWait); w.talkoot.turns.Load() > 0; {
		if time.Now().After(deadline) {
			w.diagf("talkoot: a member turn did not report its cost before shutdown")
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	w.talkoot.mu.Lock()
	// From here no member turn opens, so each run's open count below holds
	// every turn that could still report.
	w.talkoot.closing = true
	runs := w.talkoot.runs
	w.talkoot.runs = nil
	// The seats stay, revoked, so a late turn still finds its seat and sees
	// closing.
	seats := maps.Clone(w.talkoot.seats)
	w.talkoot.mu.Unlock()
	for _, b := range seats {
		b.revoke()
	}
	for _, r := range runs {
		r.mu.Lock()
		if !r.closed {
			w.talkoot.mu.Lock()
			open := maps.Clone(r.open)
			w.talkoot.mu.Unlock()
			for member, n := range open {
				for range n {
					if err := r.router.TurnUnreported(member, "the member's turn had not reported its cost when the daemon stopped"); err != nil {
						w.diagf("talkoot %s: could not record the unreported turn of %s: %v", r.id, member, err)
					}
				}
			}
		}
		r.closed = true
		r.mu.Unlock()
		r.lock.Release()
	}
}
