package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/core/permission"
	"terva.sh/terva/packages/privfs"
)

// talkootSummary is one talkoot in a list.
type talkootSummary struct {
	ID      string
	Home    string
	Running bool
	// Problem says why a talkoot homed here is not running.
	Problem string
}

// talkootMemberView is one member as a client sees it.
type talkootMemberView struct {
	Member  talkoot.Member
	Status  talkoot.Status
	Session string
}

// talkootView is a running talkoot.
type talkootView struct {
	Roster talkoot.Roster
	// Text is the talkoot.md the roster came from. An update writes the file
	// under the run's write lock, so a read under its read lock matches Roster.
	Text    []byte
	Members []talkootMemberView
	// Held names each delivery that waits for a resume or a working slot.
	Held []string
}

// talkootRoomPage is a page of the room, oldest line first.
type talkootRoomPage struct {
	Lines []talkoot.Line
	// Next is the Before that reads the page before this one, or 0 at the
	// start of the room.
	Next  int
	Total int
}

const (
	talkootPageDefault = 50
	talkootPageMax     = 500
)

// talkootList returns every talkoot, running or not.
func (w *Workspace) talkootList(ctx context.Context) ([]talkootSummary, error) {
	ids, err := talkoot.List()
	if err != nil {
		return nil, err
	}
	w.talkoot.mu.Lock()
	defer w.talkoot.mu.Unlock()
	out := make([]talkootSummary, 0, len(ids))
	for _, id := range ids {
		sum := talkootSummary{ID: id, Problem: w.talkoot.problems[id]}
		if run := w.talkoot.runs[id]; run != nil {
			sum.Running = true
			sum.Home = run.roster.Load().Home
		} else if r, err := talkoot.Load(id, talkootEnv()); err == nil {
			sum.Home = r.Home
		} else if sum.Problem == "" {
			sum.Problem = err.Error()
		}
		out = append(out, sum)
	}
	return out, nil
}

// talkootRunOf returns a talkoot this workspace runs.
func (w *Workspace) talkootRunOf(id string) (*talkootRun, error) {
	w.talkoot.mu.Lock()
	run := w.talkoot.runs[id]
	w.talkoot.mu.Unlock()
	if run != nil {
		return run, nil
	}
	if !talkoot.ValidID(id) {
		return nil, ErrTalkootNotFound
	}
	if _, err := os.Stat(filepath.Join(talkoot.Dir(), id, talkoot.FileName)); err != nil {
		return nil, ErrTalkootNotFound
	}
	return nil, ErrTalkootNotHere
}

// talkootGet returns a running talkoot's roster, members, and held deliveries.
func (w *Workspace) talkootGet(ctx context.Context, id string) (talkootView, error) {
	run, err := w.talkootRunOf(id)
	if err != nil {
		return talkootView{}, err
	}
	var v talkootView
	err = run.do(func(rt *talkoot.Router) error {
		v.Roster = *run.roster.Load()
		// A read that fails leaves Text empty. The roster still describes the
		// talkoot.
		v.Text, _ = os.ReadFile(filepath.Join(run.dir, talkoot.FileName))
		status := map[string]talkoot.Status{}
		for _, st := range rt.Statuses() {
			status[st.Member] = st
		}
		w.talkoot.mu.Lock()
		for _, m := range rt.Members() {
			v.Members = append(v.Members, talkootMemberView{Member: m, Status: status[m.ID], Session: run.seats[m.ID]})
		}
		w.talkoot.mu.Unlock()
		v.Held = rt.Held()
		return nil
	})
	return v, err
}

// talkootCreate makes a talkoot from the text of its talkoot.md and starts it.
// Its home must be this workspace's directory.
func (w *Workspace) talkootCreate(ctx context.Context, id string, text []byte) (talkootView, error) {
	r, err := w.parseRoster(id, text)
	if err != nil {
		return talkootView{}, err
	}
	if err := privfs.MkdirAll(talkoot.Dir()); err != nil {
		return talkootView{}, err
	}
	dir := filepath.Join(talkoot.Dir(), id)
	// Mkdir, not MkdirAll: the directory claims the id.
	if err := os.Mkdir(dir, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return talkootView{}, fmt.Errorf("%w: %s already exists", ErrTalkootExists, id)
		}
		return talkootView{}, err
	}
	undo := func(err error) (talkootView, error) {
		_ = os.RemoveAll(dir)
		return talkootView{}, err
	}
	if err := privfs.WriteFile(filepath.Join(dir, talkoot.FileName), text); err != nil {
		return undo(err)
	}
	if err := talkoot.CreateRoom(dir); err != nil {
		return undo(err)
	}
	if err := w.startTalkoot(r.ID); err != nil {
		return undo(err)
	}
	return w.talkootGet(ctx, id)
}

// parseRoster checks a roster the way Load does, and the rules this
// workspace adds: it is homed here, and it has no native member in a worktree.
func (w *Workspace) parseRoster(id string, text []byte) (talkoot.Roster, error) {
	return w.parseRosterEnv(id, text, talkootEnv())
}

// parseRosterEnv is parseRoster against env, which a recruiter's proposal
// widens by the persona it drafted.
func (w *Workspace) parseRosterEnv(id string, text []byte, env talkoot.Env) (talkoot.Roster, error) {
	if !config.TalkootEnabled() {
		return talkoot.Roster{}, talkoot.ErrDisabled
	}
	// 🚨 The id becomes a directory under talkoot.Dir. Validate skips an empty
	// id, so the pattern is checked here, before any path is joined.
	if !talkoot.ValidID(id) {
		return talkoot.Roster{}, fmt.Errorf("talkoot: id %q must be lower case letters, digits, and dashes, starting with a letter", id)
	}
	r, err := talkoot.Parse(text, id+"/"+talkoot.FileName)
	if err != nil {
		return talkoot.Roster{}, err
	}
	r.ID = id
	if err := talkoot.Validate(r, env); err != nil {
		return talkoot.Roster{}, err
	}
	if !sameDir(r.Home, w.cwd) {
		return talkoot.Roster{}, fmt.Errorf("talkoot: home %s is not this workspace's directory, %s", r.Home, w.cwd)
	}
	for _, m := range r.Members {
		if m.Driver == talkoot.DriverNative && m.Workspace == talkoot.WorkspaceWorktree {
			return talkoot.Roster{}, fmt.Errorf("talkoot: native member %s cannot run in a worktree yet", m.ID)
		}
	}
	return r, nil
}

// talkootUpdate replaces a running talkoot's roster with a person's edit.
//
// The router is rebuilt over the same room, so spend, pauses, and chains carry
// over. A member that left loses its seat. A member whose persona, model, or
// tier changed gets a fresh session on its next delivery. A posture change
// applies to a live session at once.
//
// 🔑 The swap is the one commit point, under run.mu. Before it, nothing a
// member can use has changed. At it, the router, the seats, and each gate's
// posture change together, so no delivery sees the new roster with an old
// member setup. The revokes wait until the lock is released, because a
// revoke waits for a seat call, and a seat call waits for that lock.
func (w *Workspace) talkootUpdate(ctx context.Context, id, by string, text []byte) (talkootView, error) {
	// The roster line records by, and the router checks no roster line.
	if !talkoot.ValidPerson(strings.TrimPrefix(by, talkoot.HumanPrefix)) {
		return talkootView{}, fmt.Errorf("talkoot: %q must name a person in 1 to 64 letters, digits, and . _ @ -", by)
	}
	run, err := w.talkootRunOf(id)
	if err != nil {
		return talkootView{}, err
	}
	next, err := w.parseRoster(id, text)
	if err != nil {
		return talkootView{}, err
	}
	run.update.Lock()
	defer run.update.Unlock()
	return w.applyRosterLocked(ctx, run, by, text, next, rosterSource{})
}

// rosterSource names the proposal a roster change came from, and whether the
// person replaced its operations. It is empty for a person's own edit.
type rosterSource struct {
	proposal, proposer string
	edited             bool
}

// applyRosterLocked commits a validated roster: the room lines, the file, the
// router, the seats, and each posture. The caller holds run.update.
func (w *Workspace) applyRosterLocked(ctx context.Context, run *talkootRun, by string, text []byte, next talkoot.Roster, from rosterSource) (talkootView, error) {
	id := run.id
	prev := *run.roster.Load()
	// The text that holds now, named by the line that corrects a failed
	// update. Only that line reads it, so an unreadable file leaves its
	// reference empty rather than stopping the update.
	prevText, prevErr := os.ReadFile(filepath.Join(run.dir, talkoot.FileName))
	if !sameDir(prev.Home, next.Home) {
		return talkootView{}, errors.New("talkoot: an update cannot move the talkoot's home")
	}

	// Sort each seated member: it keeps its session with a new posture or a
	// new tools list, or it loses its seat. A postureChange with no posture
	// is a tools change.
	type postureChange struct {
		sid, to string
		s       *wsSession
	}
	var postures []postureChange
	leaving := map[string]string{} // member id -> session id
	w.talkoot.mu.Lock()
	for _, old := range prev.Members {
		sid := run.seats[old.ID]
		if sid == "" {
			continue
		}
		m, kept := memberOf(next, old.ID)
		switch {
		case !kept || m.Driver != talkoot.DriverNative || m.Persona != old.Persona || m.Model != old.Model || m.Tier != old.Tier:
			leaving[old.ID] = sid
		case m.Posture != old.Posture:
			// setApproval rebuilds the tool view, so it narrows to a new
			// tools list too.
			postures = append(postures, postureChange{sid: sid, to: m.Posture})
		case !slices.Equal(m.Tools, old.Tools):
			postures = append(postures, postureChange{sid: sid})
		}
	}
	w.talkoot.mu.Unlock()
	// Outside w.talkoot.mu, because existing takes w.mu, which comes first.
	for i := range postures {
		postures[i].s = w.existing(postures[i].sid)
	}

	// setPostures applies each new posture in full: the args, the gate, and
	// the tool view. A tools change rebuilds the view alone. It runs after the
	// commit, so the rebuild reads the new roster.
	setPostures := func() {
		for _, c := range postures {
			if c.s == nil {
				continue
			}
			if c.to == "" {
				c.s.rebuildTools("talkoot-tools")
				continue
			}
			if err := c.s.setApproval(c.to); err != nil {
				w.diagf("talkoot %s: posture for session %s: %v", id, c.s.id, err)
				// A failed posture must not leave a narrower tools list
				// unapplied. setApproval fails only on a mode it cannot
				// parse, which a validated roster never holds.
				c.s.rebuildTools("talkoot-tools")
			}
		}
	}

	run.mu.Lock()
	if run.closed || run.stopping {
		run.mu.Unlock()
		return talkootView{}, ErrTalkootClosed
	}
	rt, err := talkoot.NewRouter(next, run.room, w.talkootDriversFor(run), talkoot.DefaultLimits(), nil)
	if err != nil {
		run.mu.Unlock()
		return talkootView{}, err
	}
	// 🚨 The seat lines go before the roster file. The restart reads seats
	// from the room, so a new roster over old seats would put a changed
	// member back in its old session. A leaver gets one too, so a member
	// added back later does not reclaim its old session. When a later step
	// fails, restore seals each old seat again, so a restart keeps it.
	var sealed []string
	rosterSealed := false
	abort := func(err error) (talkootView, error) {
		rt.Stop()
		if rosterSealed {
			// The roster line named a change that did not happen. This line
			// says so, and names the roster that still holds.
			ref := ""
			if prevErr == nil {
				ref = talkoot.RosterRevision(prevText)
			}
			if rerr := run.room.Append(talkoot.Line{Type: talkoot.LineRoster, At: time.Now(), By: humanBy(by),
				Ref: ref, Reason: "the update failed, and the roster is unchanged"}); rerr != nil {
				w.diagf("talkoot %s: could not record that the roster update failed: %v", id, rerr)
			}
		}
		for _, member := range sealed {
			if rerr := run.room.Append(talkoot.Line{Type: talkoot.LineSeat, At: time.Now(), Member: member, Ref: leaving[member]}); rerr != nil {
				// ⚠️ The member keeps its seat until this process stops, and
				// then gets a fresh session. It never gets a wrong one.
				w.diagf("talkoot %s: could not restore the seat of %s: %v", id, member, rerr)
			}
		}
		run.mu.Unlock()
		return talkootView{}, err
	}
	// The roster line records who changed the roster. It is part of the
	// update: an update the room cannot record does not happen.
	if err := run.room.Append(talkoot.Line{Type: talkoot.LineRoster, At: time.Now(), By: humanBy(by), Ref: talkoot.RosterRevision(text),
		Proposal: from.proposal, Proposer: from.proposer, Edited: from.edited, Changes: talkoot.Diff(prev, next)}); err != nil {
		return abort(fmt.Errorf("talkoot: the room could not record the roster change: %w", err))
	}
	rosterSealed = true
	for member := range leaving {
		if err := run.room.Append(talkoot.Line{Type: talkoot.LineSeat, At: time.Now(), Member: member}); err != nil {
			return abort(fmt.Errorf("talkoot: the room could not record that %s lost its seat: %w", member, err))
		}
		sealed = append(sealed, member)
	}
	// 🚨 The file is written last, once nothing can fail. A roster on disk
	// that the running router refused would apply at the next start, after
	// the caller was told the update failed.
	if err := privfs.WriteFile(filepath.Join(run.dir, talkoot.FileName), text); err != nil {
		return abort(err)
	}
	run.router.Stop()
	run.router = rt
	run.roster.Store(&next)
	// The commit. The gate takes each new posture here, so the first
	// delivery through the new router already runs under it, and a failed
	// update changed none. The gate decides every call. The args and the
	// tool view follow after the lock.
	for _, c := range postures {
		if c.s == nil || c.s.gate == nil {
			continue
		}
		if mode, err := permission.ParseApprovalMode(c.to); err == nil {
			c.s.gate.SetMode(mode)
		}
	}
	// A member that lost its seat gets a fresh session on its next delivery,
	// and no delivery can run until this lock is released. Its old binding
	// retires here, so no call through the new router passes it.
	unseated := map[string]*seatBinding{}
	w.talkoot.mu.Lock()
	for member, sid := range leaving {
		delete(run.seats, member)
		if b := w.talkoot.seats[sid]; b != nil {
			b.retired.Store(true)
			unseated[sid] = b
		}
		delete(w.talkoot.seats, sid)
	}
	w.talkoot.mu.Unlock()
	run.mu.Unlock()

	for sid, b := range unseated {
		b.revoke()
		if s := w.existing(sid); s != nil {
			s.rebuildTools("talkoot")
		}
	}
	setPostures()
	if err := run.do(func(rt *talkoot.Router) error {
		rt.Release()
		return nil
	}); err != nil {
		return talkootView{}, err
	}
	return w.talkootGet(ctx, id)
}

// talkootPost posts a person's message to the talkoot. An empty to reaches
// the coordinator.
func (w *Workspace) talkootPost(ctx context.Context, id, person string, to []string, body string, refs []string, thread string) (talkoot.Envelope, error) {
	run, err := w.talkootRunOf(id)
	if err != nil {
		return talkoot.Envelope{}, err
	}
	var e talkoot.Envelope
	err = run.do(func(rt *talkoot.Router) (err error) {
		e, err = rt.Post(strings.TrimPrefix(person, talkoot.HumanPrefix), to, body, refs, thread)
		return err
	})
	return e, err
}

// talkootRoom returns a page of the room, reading back from before. A zero
// before reads the newest page.
func (w *Workspace) talkootRoom(ctx context.Context, id string, before, limit int) (talkootRoomPage, error) {
	run, err := w.talkootRunOf(id)
	if err != nil {
		return talkootRoomPage{}, err
	}
	// The run's own room: a second Room over the file would keep its own
	// place in the chain.
	lines, err := run.room.Read()
	if err != nil {
		return talkootRoomPage{}, err
	}
	if limit <= 0 {
		limit = talkootPageDefault
	}
	limit = min(limit, talkootPageMax)
	end := len(lines)
	if before > 0 && before < end {
		end = before
	}
	start := max(0, end-limit)
	return talkootRoomPage{Lines: lines[start:end], Next: start, Total: len(lines)}, nil
}

// talkootPause pauses a member, a chain, or the whole talkoot for a person.
func (w *Workspace) talkootPause(ctx context.Context, id, by, member, chain, reason string) error {
	run, err := w.talkootRunOf(id)
	if err != nil {
		return err
	}
	return run.do(func(rt *talkoot.Router) error { return rt.Pause(humanBy(by), member, chain, reason) })
}

// talkootResume lifts a person's pause, or a guard's.
func (w *Workspace) talkootResume(ctx context.Context, id, by, member, chain string) error {
	run, err := w.talkootRunOf(id)
	if err != nil {
		return err
	}
	return run.do(func(rt *talkoot.Router) error { return rt.Resume(humanBy(by), member, chain) })
}

// talkootWatch calls fn with each event of one talkoot until stop is called.
// fn runs on the goroutine that caused the event, so it must not block.
func (w *Workspace) talkootWatch(id string, fn func(talkootEvent)) (stop func()) {
	w.talkoot.mu.Lock()
	defer w.talkoot.mu.Unlock()
	if w.talkoot.watchers == nil {
		w.talkoot.watchers = map[string]map[int]func(talkootEvent){}
	}
	if w.talkoot.watchers[id] == nil {
		w.talkoot.watchers[id] = map[int]func(talkootEvent){}
	}
	w.talkoot.nextWatch++
	n := w.talkoot.nextWatch
	w.talkoot.watchers[id][n] = fn
	return func() {
		w.talkoot.mu.Lock()
		defer w.talkoot.mu.Unlock()
		delete(w.talkoot.watchers[id], n)
	}
}

func humanBy(by string) string {
	if strings.HasPrefix(by, talkoot.HumanPrefix) {
		return by
	}
	return talkoot.HumanPrefix + by
}
