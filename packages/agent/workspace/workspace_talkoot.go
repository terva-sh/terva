package workspace

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"terva.sh/terva/packages/agent/persona"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/agent/worker"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// talkootEnv resolves a roster's names against this binary: the persona
// library, the worker registry, and the swarm tiers. The talkoot package cannot
// build this itself, because the registry and the tiers import the swarm.
func talkootEnv() talkoot.Env {
	return talkoot.Env{
		PersonaExists: func(ref string) bool {
			_, ok := persona.Lookup(ref)
			return ok
		},
		Driver: func(name string) (bool, error) {
			b, err := worker.Lookup(name)
			if err != nil {
				return false, err
			}
			return b.ReportsCost, nil
		},
		Tiers: tools.SwarmTierNames(),
		DriverTools: func(driver string, list []string) error {
			b, err := worker.Lookup(driver)
			if err != nil {
				return err
			}
			if b.Tools == nil {
				return errors.New("the backend has no allowlist that covers every tool")
			}
			_, err = b.Tools(list)
			return err
		},
	}
}

// memberBinding names what runs a member of a talkoot: a session id for a
// native member, a swarm agent id for a worker member. The error says why a
// member has nothing bound, and the router records it on the delivery. Native
// members get a session on their first delivery (memberSession). Worker
// members get stable agent ids in TKT-01M396QY3.
type memberBinding func(talkootID string, m talkoot.Member) (id string, err error)

// wsTalkoot holds the talkoots this workspace runs, and the seat of each
// native member's session.
type wsTalkoot struct {
	mu    sync.Mutex
	seats map[string]*seatBinding // by session id
	runs  map[string]*talkootRun  // by talkoot id
	// carriers park the asks of each talkoot's workers, by talkoot id. A
	// carrier outlives its run, so an ask that waits while the run loads
	// again stays in the inbox and can still be answered.
	carriers map[string]*wsSession
	// members holds every session that held a seat in a talkoot homed here,
	// seated now or not. None of them is the person's default session.
	members map[string]bool
	// foldOnce reads the seat lines of the talkoots homed here that this
	// workspace does not run, once, for the terminal beside a daemon.
	foldOnce sync.Once
	// folded holds each room's size at its last fold, by talkoot id. A room
	// only grows, so a room at the same size holds no new seat line.
	folded map[string]int64
	// foldErrs holds the last read failure of each room the fold could not
	// read, so a repeated fold reports it once.
	foldErrs map[string]string
	// problems says why a talkoot homed here did not start.
	problems  map[string]string
	watchers  map[string]map[int]func(talkootEvent)
	nextWatch int
	// stopping is set when shutdown begins. No talkoot starts after it.
	stopping bool
	// closing is set when closeTalkoots takes the runs. No member turn opens
	// after it.
	closing bool
	// turns counts member turns that have not reported their cost yet, so
	// Close can wait for them. A counter, not a WaitGroup: a turn can start
	// while Close waits, and a WaitGroup forbids that.
	turns atomic.Int64
}

// seatBinding is a native member's place in a talkoot: the member its session
// speaks as, and the run it speaks through. Each seating makes a new one,
// and an unseat or a reseat revokes the old one. An update swaps the run's
// router and keeps the binding.
type seatBinding struct {
	run    *talkootRun
	member string
	// mu is held for reading through each call and for writing to revoke,
	// so a revoke waits for a call in flight and no call starts after it.
	mu      sync.RWMutex
	revoked bool
	// retired is set under the run's write lock when an update takes the
	// seat away. A call checks it inside the router call, so no call through
	// the new router passes, even one that got past revoked before the
	// update. The revoke itself waits until the run's lock is released.
	retired atomic.Bool
	// personSends counts the envelopes this seat sent to a person. A turn that
	// raised it already answered the person in its own words, so its final
	// prose is not mirrored too (talkootTurn).
	personSends atomic.Uint64
}

// revoke stops the binding. It waits for a call in flight to finish.
func (b *seatBinding) revoke() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.revoked = true
	b.mu.Unlock()
}

// talkootSeat is what a session's Talkoot tools hold: the binding the session
// had when the tools were made.
//
// 🚨 A turn can hold a tool instance from before a rebuild. So each call holds
// the binding's read lock and refuses a revoked binding. An unseat, or a seat
// as another member, then stops every older instance, and no call slips in
// between the check and the router.
//
// w is the workspace the seat proposes through. Every seat carries it, so no
// path can hand the tools a seat that panics on a proposal.
type talkootSeat struct {
	b *seatBinding
	w *Workspace
}

var errSeatRevoked = errors.New("this session no longer holds that seat in the talkoot")

func (s talkootSeat) Send(o talkoot.Outgoing) (talkoot.Envelope, error) {
	ctx := context.Background()
	ho, err := s.w.checkTicketHandoff(ctx, s.b, o)
	if err != nil {
		return talkoot.Envelope{}, err
	}
	e, err := s.send(o)
	if err != nil {
		return e, err
	}
	return e, ho.move(ctx, s.w.cwd, e)
}

func (s talkootSeat) send(o talkoot.Outgoing) (talkoot.Envelope, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	if s.b.revoked {
		return talkoot.Envelope{}, errSeatRevoked
	}
	var e talkoot.Envelope
	err := s.b.run.do(func(rt *talkoot.Router) (err error) {
		if s.b.retired.Load() {
			return errSeatRevoked
		}
		e, err = rt.Send(s.b.member, o)
		return err
	})
	if err == nil && slices.ContainsFunc(e.To, func(to string) bool { return strings.HasPrefix(to, talkoot.HumanPrefix) }) {
		s.b.personSends.Add(1)
	}
	return e, err
}

// publishReply uses the binding that opened the turn. A reseat cannot make
// an old turn speak as a new member. Cost reporting still uses the old seat.
func (s talkootSeat) publishReply(body string) error {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	if s.b.revoked {
		return nil
	}
	return s.b.run.do(func(rt *talkoot.Router) error {
		if s.b.retired.Load() {
			return nil
		}
		_, err := rt.PublishReply(s.b.member, body)
		if errors.Is(err, talkoot.ErrNoHumanRoot) || errors.Is(err, talkoot.ErrDuplicate) {
			// Private-session work has no room root. A duplicate already exists.
			return nil
		}
		return err
	})
}

// finalTalkootText selects only the completed final assistant message.
// Tool-step commentary and reasoning never become a room reply.
func finalTalkootText(msgs []provider.Message) string {
	if len(msgs) == 0 || msgs[len(msgs)-1].Role != provider.RoleAssistant {
		return ""
	}
	m := msgs[len(msgs)-1]
	if m.Meta[core.MetaIncomplete] == "true" {
		return ""
	}
	for _, c := range m.Content {
		if _, tool := c.(provider.ToolCallBlock); tool {
			return ""
		}
	}
	return assistantVisibleText(m)
}

// Answer records a person's answer to this seat's question in the room, and
// returns its id.
func (s talkootSeat) Answer(qs []talkoot.Answered) (string, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	if s.b.revoked {
		return "", errSeatRevoked
	}
	var id string
	err := s.b.run.do(func(rt *talkoot.Router) (err error) {
		if s.b.retired.Load() {
			return errSeatRevoked
		}
		id, err = rt.Answer(s.b.member, qs)
		return err
	})
	return id, err
}

func (s talkootSeat) Roster() ([]tools.TalkootRosterEntry, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	if s.b.revoked {
		return nil, errSeatRevoked
	}
	var out []tools.TalkootRosterEntry
	err := s.b.run.do(func(rt *talkoot.Router) error {
		if s.b.retired.Load() {
			return errSeatRevoked
		}
		status := map[string]talkoot.Status{}
		for _, st := range s.b.run.overlay(rt.Statuses()) {
			status[st.Member] = st
		}
		for _, m := range rt.Members() {
			out = append(out, tools.TalkootRosterEntry{Member: m, Status: status[m.ID], Self: m.ID == s.b.member})
		}
		return nil
	})
	return out, err
}

// WriteNote writes one of the seat's member's notes. The seat names the
// author, so a member writes only under its own id.
func (s talkootSeat) WriteNote(name, text string) (string, error) {
	var ref string
	err := s.notes(func(dir string) (err error) {
		ref, err = talkoot.WriteNote(dir, s.b.member, name, text)
		return err
	})
	return ref, err
}

// ReadNote reads any member's note in the seat's talkoot.
func (s talkootSeat) ReadNote(ref string) (string, error) {
	var text string
	err := s.notes(func(dir string) (err error) {
		text, err = talkoot.ReadNote(dir, ref)
		return err
	})
	return text, err
}

// ListNotes lists the notes in the seat's talkoot.
func (s talkootSeat) ListNotes() ([]talkoot.Note, error) {
	var out []talkoot.Note
	err := s.notes(func(dir string) (err error) {
		out, err = talkoot.ListNotes(dir)
		return err
	})
	return out, err
}

// notes runs fn on the talkoot's directory under the checks a send passes: a
// revoked or retired seat refuses, and a closed run refuses.
func (s talkootSeat) notes(fn func(dir string) error) error {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	if s.b.revoked {
		return errSeatRevoked
	}
	return s.b.run.do(func(*talkoot.Router) error {
		if s.b.retired.Load() {
			return errSeatRevoked
		}
		return fn(s.b.run.dir)
	})
}

// TurnEnded reports the cost of the member's turn to the router, which frees
// its working slot and releases what waited for one.
//
// 🔑 It reports on a revoked seat too. The turn began under the seat and
// spent as the member, so its spend must reach the caps. A delete or an
// update can revoke the seat mid-turn, and a refusal would also leave the
// member working for good.
func (s talkootSeat) TurnEnded(costUSD float64) error {
	return s.turnEnded(costUSD, "", false, nil)
}

// turnEnded is TurnEnded, and it calls settled inside the router call once
// the router has the report. A turn with a failure pauses the member with
// the kind failed. An interrupted turn writes a turn line that says so.
func (s talkootSeat) turnEnded(costUSD float64, failed string, interrupted bool, settled func()) error {
	return s.b.run.do(func(rt *talkoot.Router) error {
		var err error
		switch {
		case failed != "":
			err = rt.TurnFailed(s.b.member, costUSD, failureReason(failed))
		case interrupted:
			err = rt.TurnInterrupted(s.b.member, costUSD)
		default:
			err = rt.TurnEnded(s.b.member, costUSD)
		}
		if settled != nil {
			settled()
		}
		return err
	})
}

// read reports that the member's turn read a delivery. A revoked seat
// reports nothing: the member works on in another session, or nowhere.
func (s talkootSeat) read(r talkoot.Receipt) error {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	if s.b.revoked {
		return nil
	}
	return s.b.run.do(func(rt *talkoot.Router) error {
		if s.b.retired.Load() {
			return nil
		}
		return rt.Read(r)
	})
}

// seatTalkoot gives a session a seat in a talkoot, and re-derives its tools
// so that the Talkoot tools appear.
func (w *Workspace) seatTalkoot(sessID string, run *talkootRun, member string) {
	w.talkoot.mu.Lock()
	if w.talkoot.seats == nil {
		w.talkoot.seats = map[string]*seatBinding{}
	}
	old := w.talkoot.seats[sessID]
	w.talkoot.seats[sessID] = &seatBinding{run: run, member: member}
	w.talkoot.markMemberLocked(sessID)
	w.talkoot.mu.Unlock()
	old.revoke()
	if s := w.existing(sessID); s != nil {
		s.rebuildTools("talkoot")
	}
}

// unseatTalkoot takes a session's seat away, and its Talkoot tools with it.
func (w *Workspace) unseatTalkoot(sessID string) {
	w.talkoot.mu.Lock()
	old := w.talkoot.seats[sessID]
	delete(w.talkoot.seats, sessID)
	w.talkoot.mu.Unlock()
	old.revoke()
	if s := w.existing(sessID); s != nil {
		s.rebuildTools("talkoot")
	}
}

// talkootActivity records a tool call a seated session starts, or with an
// empty name the end of one. An empty id with an empty name ends every call,
// for a turn that ended. A session with no seat, or a revoked one, records
// nothing.
func (w *Workspace) talkootActivity(sessID, callID, tool string) {
	// ⚠️ This runs on the agent's event path of every session, so it must not
	// wait on either lock. A revoke holds, or waits for, the seat's write
	// lock, and an RLock queues behind it. The tool is only a view, so a busy
	// lock skips the update, and the router clears every call when the turn
	// ends.
	if !w.talkoot.mu.TryLock() {
		return
	}
	b := w.talkoot.seats[sessID]
	w.talkoot.mu.Unlock()
	if b == nil {
		return
	}
	if !b.mu.TryRLock() {
		return
	}
	defer b.mu.RUnlock()
	if b.revoked {
		return
	}
	_ = b.run.do(func(rt *talkoot.Router) error {
		if tool != "" {
			rt.ToolStarted(b.member, callID, tool)
		} else {
			rt.ToolEnded(b.member, callID)
		}
		return nil
	})
}

// talkootTurn opens a seated session's turn and captures its end and reply
// callbacks. The reply callback runs only after a successful generation,
// before the session releases its turn slot. The end reports cost, failure,
// and interruption. Close waits for every open turn to end. An unseated
// session gets nil callbacks.
//
// The run counts the member's open turns apart from its roster, so shutdown
// finds a turn whose member an update removed. The count drops inside the
// router call that takes the report. Under the run's write lock, the count is
// then exactly the turns the router never heard end.
//
// 🚨 The seat, the closing check, and both counts share one hold of
// w.talkoot.mu, which closeTalkoots also holds to set closing. A turn is
// either counted before the talkoots close, or refused with closing true, and
// then the caller does not run it: no report of it could reach a closed run.
func (w *Workspace) talkootTurn(sessID string) (end func(costUSD float64, ran bool, failed string, interrupted bool), publish func(string), closing bool) {
	w.talkoot.mu.Lock()
	b := w.talkoot.seats[sessID]
	if b == nil {
		w.talkoot.mu.Unlock()
		return nil, nil, false
	}
	if w.talkoot.closing {
		w.talkoot.mu.Unlock()
		return nil, nil, true
	}
	seat, run, member := talkootSeat{b: b, w: w}, b.run, b.member
	if run.open == nil {
		run.open = map[string]int{}
	}
	run.open[member]++
	w.talkoot.turns.Add(1)
	w.talkoot.mu.Unlock()
	// The router sees the member as working from here, though a person may
	// have started this turn and no delivery did (TKT-01M3SMDS89).
	run.nativeTurn(member, 1)
	run.flush()
	sentBefore := b.personSends.Load()
	publish = func(body string) {
		// 🚨 A turn that already messaged a person would post twice: once in
		// its chosen words and again as its final prose, which the body
		// dedupe misses whenever the two differ.
		if b.personSends.Load() != sentBefore {
			return
		}
		if err := seat.publishReply(body); err != nil {
			w.diagf("talkoot: session %s could not publish its reply: %v", sessID, err)
		}
	}
	return func(costUSD float64, ran bool, failed string, interrupted bool) {
		defer w.talkoot.turns.Add(-1)
		done := false
		settle := func() {
			if done {
				return
			}
			done = true
			w.talkoot.mu.Lock()
			if run.open[member]--; run.open[member] <= 0 {
				delete(run.open, member)
			}
			w.talkoot.mu.Unlock()
		}
		// A turn that did not run, or whose report the router refused, still
		// closes here.
		defer settle()
		// 🔑 The member stops counting as busy before the router hears the
		// turn end, so the release that the report makes sees the slot free.
		run.nativeTurn(member, -1)
		if !ran {
			// A delivery may have waited on this turn's slot, and no report
			// releases it.
			_ = run.do(func(rt *talkoot.Router) error {
				rt.Release()
				return nil
			})
			return
		}
		if err := seat.turnEnded(costUSD, failed, interrupted, settle); err != nil {
			w.diagf("talkoot: session %s could not report its turn: %v", sessID, err)
		}
	}, publish, false
}

func (t *wsTalkoot) markMemberLocked(sessID string) {
	if t.members == nil {
		t.members = map[string]bool{}
	}
	t.members[sessID] = true
}

func (w *Workspace) talkootSeatOf(sessID string) (talkootSeat, bool) {
	w.talkoot.mu.Lock()
	defer w.talkoot.mu.Unlock()
	b, ok := w.talkoot.seats[sessID]
	return talkootSeat{b: b, w: w}, ok
}

// talkootNativeDriver delivers to a native member through its session's
// queue, the path a person's queued message takes. A busy session receives
// the text at its next safe boundary, and an idle one starts a turn. It
// reports when the turn reads the text, so the member takes the delivery's
// chain then (TKT-01M3ADB5).
type talkootNativeDriver struct {
	sessionOf memberBinding
	resolve   func(id string) (*wsSession, error)
	// read reports a delivery the session's turn has read. DeliverRead
	// refuses to run without it.
	read func(sessID, member string, r talkoot.Receipt)
	// busy reports whether the member's session runs a turn. Nil reports
	// none.
	busy func(member string) bool
}

var (
	_ talkoot.ReadDriver = talkootNativeDriver{}
	_ talkoot.BusyDriver = talkootNativeDriver{}
)

// Busy makes the native driver a talkoot.BusyDriver, so the router counts a
// turn that a person started in the member's session.
func (d talkootNativeDriver) Busy(member string) bool { return d.busy != nil && d.busy(member) }

func (d talkootNativeDriver) Deliver(talkootID string, m talkoot.Member, text string) error {
	return d.deliver(talkootID, m, text, nil)
}

func (d talkootNativeDriver) DeliverRead(talkootID string, m talkoot.Member, text string, r talkoot.Receipt) error {
	return d.deliver(talkootID, m, text, &r)
}

func (d talkootNativeDriver) deliver(talkootID string, m talkoot.Member, text string, r *talkoot.Receipt) error {
	id, err := d.sessionOf(talkootID, m)
	if err != nil {
		return fmt.Errorf("member %s: %w", m.ID, err)
	}
	s, err := d.resolve(id)
	if err != nil {
		return fmt.Errorf("member %s: %w", m.ID, err)
	}
	if r == nil {
		s.queue(text)
		return nil
	}
	// 🚨 The router leaves the chain alone for a ReadDriver. A delivery whose
	// read nobody reports would never move the member, so it fails instead.
	if d.read == nil {
		return fmt.Errorf("member %s: the native driver reports no reads", m.ID)
	}
	receipt := *r
	s.queueTalkoot(text, receipt.FromPerson(), func() { d.read(s.id, m.ID, receipt) })
	return nil
}

// talkootWorkerDriver delivers to a worker member: it spawns the member's
// worker, or sends the text as the worker's next user turn.
type talkootWorkerDriver struct {
	deliver func(m talkoot.Member, text string, r *talkoot.Receipt) error
}

func (d talkootWorkerDriver) Deliver(_ string, m talkoot.Member, text string) error {
	if err := d.deliver(m, text, nil); err != nil {
		return fmt.Errorf("member %s: %w", m.ID, err)
	}
	return nil
}

// DeliverRead makes the worker driver a talkoot.ReadDriver. The worker takes
// the envelope's chain when its turn reads the text, on a backend that says
// when, and when the worker accepts the text on any other.
func (d talkootWorkerDriver) DeliverRead(_ string, m talkoot.Member, text string, r talkoot.Receipt) error {
	if err := d.deliver(m, text, &r); err != nil {
		return fmt.Errorf("member %s: %w", m.ID, err)
	}
	return nil
}

// talkootDrivers wires both drivers to this workspace's sessions and swarm.
func (w *Workspace) talkootDrivers(sessionOf memberBinding, deliverWorker func(m talkoot.Member, text string, r *talkoot.Receipt) error, read func(sessID, member string, r talkoot.Receipt), busy func(member string) bool) talkoot.Drivers {
	return talkoot.Drivers{
		Native: talkootNativeDriver{sessionOf: sessionOf, resolve: w.resolve, read: read, busy: busy},
		Worker: talkootWorkerDriver{deliver: deliverWorker},
	}
}
