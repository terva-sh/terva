package workspace

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/worker"
)

// A worker member is a Talkoot member that runs on a worker backend, such as
// Claude Code, rather than in a terva session (TKT-01M396QY3). Its first
// delivery spawns the worker with that delivery as the task. Each later
// delivery is the worker's next user turn. The swarm id is recorded on the
// member's seat line, as a native member's session id is, so the member keeps
// its worker across a restart.
//
// A worker member is receive-only until the Talkoot MCP bridge lands
// (TKT-01M396QZ). It has no seat tools, so it cannot send. Its reply is the
// last message of its turn, which the room records as a note to the
// coordinator.

// workerEvents is what a worker reports back: each turn's end, with the
// worker's running cost total and the last message of the turn, and its exit,
// with the running total then. A host calls them with the worker's id.
type workerEvents struct {
	// turnEnd reports a turn's end. texts counts the user texts the turn
	// took, as the worker echoed them, and 0 when it echoed none.
	turnEnd func(id string, totalUSD float64, reply string, texts int)
	exit    func(id string, totalUSD float64)
	// tool reports a tool call the worker starts, or with an empty name the
	// end of one. It runs on the worker's event path, as the events arrive.
	tool func(callID, name string)
	// userTurn reports the text of a user turn as it enters the worker's
	// conversation, on a backend that reports reads.
	userTurn func(text string)
}

// workerHost is the swarm surface a worker member runs on. The workspace's
// swarm serves it, and a test puts a fake in its place. A host installs the
// events before the worker's runner starts, so no turn ends unheard.
type workerHost interface {
	spawn(ctx context.Context, req swarm.SpawnRequest, ev workerEvents) (string, error)
	// resume revives the agent. A dir revives it into the lease its
	// member re-acquired.
	resume(ctx context.Context, id, dir string, ev workerEvents) error
	// adopt points a live worker's reports at ev, for a run that did not
	// start it, and returns the worker's running total so far.
	adopt(id string, ev workerEvents) (float64, error)
	// state reports whether the agent exists, and whether its runner is live.
	state(id string) (exists, live bool)
	// holds returns the limits the agent was spawned with, as persisted.
	holds(id string) (swarm.SpawnRequest, bool)
	// agentSnapshot reports what the agent is doing, as the tasks pane shows it.
	agentSnapshot(id string) (swarm.AgentSnapshot, bool)
	send(id, text string) error
	stop(id string) error
	// exited waits up to d for the agent's process to end, and reports
	// whether it did. A stop returns before the process drains.
	exited(id string, d time.Duration) bool
}

// swarmWorkers is the workerHost of a real swarm.
type swarmWorkers struct{ f *swarm.Swarm }

// hook builds the swarm's hooks and starts the exit watch. The turn-end hook
// can fire before the swarm call returns the agent, so it waits for it. The
// event hook needs no agent.
func (h swarmWorkers) hook(ev workerEvents) (swarm.Hooks, func(*swarm.Agent)) {
	agent := make(chan *swarm.Agent, 1)
	var counts turnTexts
	onTurnEnd := func(_ int, errMsg string) {
		a := <-agent
		agent <- a
		counts.report(errMsg, func(texts int, errMsg string) {
			s := a.Snapshot()
			ev.turnEnd(a.ID, s.CostUSD, turnReply(s.LastAssistant, errMsg), texts)
		})
	}
	started := func(a *swarm.Agent) {
		agent <- a
		go func() {
			a.Wait()
			ev.exit(a.ID, a.Snapshot().CostUSD)
		}()
	}
	onEvent := func(e swarm.Event) {
		if ev.tool != nil {
			for _, t := range workerToolActivity(e) {
				ev.tool(t.id, t.name)
			}
		}
		taken := workerUserTexts(e)
		if ev.userTurn != nil {
			for _, text := range taken {
				ev.userTurn(text)
			}
		}
		counts.see(e, len(taken))
	}
	return swarm.Hooks{OnTurnEnd: onTurnEnd, OnEvent: onEvent}, started
}

// turnTexts counts the user texts each turn of one worker process took.
//
// 🔑 The swarm calls OnEvent in the order the events arrive, and runs each
// OnTurnEnd on a goroutine of its own, which can run in any order. So each
// turn end is taken whole on the event path: the texts its turn echoed and
// its error, queued at its end event. The callbacks then report the queue in
// that order, one at a time, whichever callback runs first. A turn's count
// and its error stay together, and a later turn cannot pay before an earlier
// one.
type turnTexts struct {
	mu    sync.Mutex
	texts int
	ended []queuedEnd
	// reporting keeps one report at a time, so reports leave in queue order.
	reporting sync.Mutex
}

// queuedEnd is one turn end the event path saw and no callback has reported.
type queuedEnd struct {
	texts  int
	errMsg string
}

// see counts the taken texts of event e, and queues the turn at its end.
func (c *turnTexts) see(e swarm.Event, taken int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.texts += taken
	if _, errMsg, ok := swarm.TaskTurnEnd(e); ok {
		c.ended = append(c.ended, queuedEnd{texts: c.texts, errMsg: errMsg})
		c.texts = 0
	}
}

// report hands fn the oldest queued turn end. When none waits, because no
// event path saw the end, it hands fn the callback's own error and no texts,
// so the turn end still reports.
func (c *turnTexts) report(errMsg string, fn func(texts int, errMsg string)) {
	c.reporting.Lock()
	defer c.reporting.Unlock()
	c.mu.Lock()
	next := queuedEnd{errMsg: errMsg}
	if len(c.ended) > 0 {
		next, c.ended = c.ended[0], c.ended[1:]
	}
	c.mu.Unlock()
	fn(next.texts, next.errMsg)
}

// workerUserTexts returns the text blocks of a user_message event: the texts
// of a user turn that entered the worker's conversation. The claude
// translator marks each user event, and one it did not echo from its input,
// such as an interrupt's marker, carries no user turn.
func workerUserTexts(e swarm.Event) []string {
	if e.Type != "user_message" {
		return nil
	}
	if replay, ok := e.Data["replay"].(bool); ok && !replay {
		return nil
	}
	msg, _ := e.Data["message"].(map[string]any)
	blocks, _ := msg["content"].([]any)
	var out []string
	for _, b := range blocks {
		m, _ := b.(map[string]any)
		if t, _ := m["type"].(string); t != "text" {
			continue
		}
		if text, _ := m["text"].(string); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// toolMark is one tool call a worker event starts, or ends when name is
// empty.
type toolMark struct{ id, name string }

// workerToolActivity reads the tool calls a worker event starts and ends.
// Two vocabularies reach the swarm. The terva wire sends tool_call and
// tool_result events. The claude translator sends the Anthropic message
// blocks: tool_use in an assistant message, tool_result in a user message.
func workerToolActivity(e swarm.Event) []toolMark {
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	switch e.Type {
	case "tool_call":
		if id, name := str(e.Data, "id"), str(e.Data, "name"); id != "" && name != "" {
			return []toolMark{{id, name}}
		}
	case "tool_result":
		if id := str(e.Data, "id"); id != "" {
			return []toolMark{{id: id}}
		}
	case "assistant_message", "user_message":
		msg, _ := e.Data["message"].(map[string]any)
		blocks, _ := msg["content"].([]any)
		var out []toolMark
		for _, b := range blocks {
			m, _ := b.(map[string]any)
			switch str(m, "type") {
			case "tool_use":
				if id, name := str(m, "id"), str(m, "name"); id != "" && name != "" {
					out = append(out, toolMark{id, name})
				}
			case "tool_result":
				if id := str(m, "tool_use_id"); id != "" {
					out = append(out, toolMark{id: id})
				}
			}
		}
		return out
	}
	return nil
}

// turnReply is what a worker's turn says to the room. A failed turn wrote no
// answer of its own, and its last answer belongs to an earlier turn, so the
// room hears the failure instead.
func turnReply(last, errMsg string) string {
	if errMsg != "" {
		return "The turn failed: " + errMsg
	}
	return last
}

func (h swarmWorkers) spawn(ctx context.Context, req swarm.SpawnRequest, ev workerEvents) (string, error) {
	hooks, started := h.hook(ev)
	req.OnTurnEnd, req.OnEvent = hooks.OnTurnEnd, hooks.OnEvent
	a, err := h.f.SpawnReq(ctx, req)
	if err != nil {
		return "", err
	}
	started(a)
	return a.ID, nil
}

func (h swarmWorkers) resume(ctx context.Context, id, dir string, ev workerEvents) error {
	hooks, started := h.hook(ev)
	a, err := h.f.ResumeIn(ctx, id, dir, hooks)
	if err != nil {
		return err
	}
	started(a)
	return nil
}

func (h swarmWorkers) adopt(id string, ev workerEvents) (float64, error) {
	a := h.f.Get(id)
	if a == nil || a.ID != id {
		return 0, fmt.Errorf("swarm: no such agent %q", id)
	}
	hooks, started := h.hook(ev)
	a.SetOnTurnEnd(hooks.OnTurnEnd)
	a.SetOnEvent(hooks.OnEvent)
	started(a)
	return a.Snapshot().CostUSD, nil
}

func (h swarmWorkers) state(id string) (bool, bool) {
	a := h.f.Get(id)
	if a == nil || a.ID != id {
		return false, false
	}
	switch a.Status() {
	case swarm.StatusRunning, swarm.StatusPending:
		return true, true
	}
	return true, false
}

func (h swarmWorkers) agentSnapshot(id string) (swarm.AgentSnapshot, bool) {
	a := h.f.Get(id)
	if a == nil || a.ID != id {
		return swarm.AgentSnapshot{}, false
	}
	return a.Snapshot(), true
}

func (h swarmWorkers) holds(id string) (swarm.SpawnRequest, bool) {
	a := h.f.Get(id)
	if a == nil || a.ID != id {
		return swarm.SpawnRequest{}, false
	}
	req := swarm.SpawnRequest{
		Backend: a.Backend, Approval: a.Approval, Tools: a.Tools, Persona: a.Persona,
		Provider: a.Provider, Model: a.Model, Reasoning: a.Reasoning, SessionID: a.SessionID,
	}
	// A reload moved the agent's dir to the home checkout, so only whether
	// it runs in its member's lease is kept, not where.
	if a.OwnerLease {
		req.Dir = a.Dir
	}
	return req, true
}

func (h swarmWorkers) send(id, text string) error { return h.f.SendUserTurn(id, text) }

func (h swarmWorkers) stop(id string) error { return h.f.Stop(id) }

func (h swarmWorkers) exited(id string, d time.Duration) bool {
	a := h.f.Get(id)
	if a == nil {
		return true
	}
	select {
	case <-a.Done():
		return true
	case <-time.After(d):
		return false
	}
}

// workers returns the host worker members run on, or nil when the workspace
// has no swarm.
func (w *Workspace) workers() workerHost {
	if w.talkootWorkers != nil {
		return w.talkootWorkers
	}
	if w.swarm == nil {
		return nil
	}
	return swarmWorkers{f: w.swarm}
}

// memberUnbound says why m has no process to run a turn, or returns nil. The
// worker driver and the introductions both read it, so both agree.
func (w *Workspace) memberUnbound(m talkoot.Member) error {
	if m.Driver == talkoot.DriverNative {
		return nil
	}
	if w.workers() == nil {
		return errors.New("the workspace has no swarm to run a worker member")
	}
	// 🚨 A worker is an outside harness with its own authority, so its spawn
	// needs the user-layer external_workers gate, as a spawn tool does.
	if err := worker.AllowSpawn(m.Driver); err != nil {
		return err
	}
	return nil
}

// workerReadyWait bounds how long a delivery waits for a revived worker's
// inbox. A spawn's first delivery is its task, so only a revival waits.
const workerReadyWait = 15 * time.Second

// deliverWorker delivers text to worker member m: it spawns the worker with
// the text as its task, or sends the text as the worker's next user turn,
// reviving the worker first when its process is gone.
//
// 🔑 The binding holds run.workerMu, and a worker's reports wait for it, so a
// report never reaches a turn the binding has not recorded yet.
//
// 🔑 The send waits outside run.workerMu. A revived worker's inbox can take
// seconds to listen, and every report of the talkoot's workers, and every
// ask, waits on that lock. The member's send lock, taken first, keeps the
// member's deliveries in the order they bound.
func (w *Workspace) deliverWorker(run *talkootRun, m talkoot.Member, text string) error {
	_, err := w.deliverWorkerTo(run, m, text, nil)
	return err
}

// deliverWorkerTo is deliverWorker, and reports whether the delivery spawned
// the worker, with the text as its task. beforeSend, when set, runs after the
// binding and just before the text is sent to a seated worker.
func (w *Workspace) deliverWorkerTo(run *talkootRun, m talkoot.Member, text string, beforeSend func()) (spawned bool, err error) {
	mu := w.memberSendMu(run, m.ID)
	mu.Lock()
	defer mu.Unlock()
	send, err := func() (func() error, error) {
		if err := w.awaitStoppedWorker(run, m.ID); err != nil {
			return nil, err
		}
		return w.bindWorker(run, m, text)
	}()
	spawned = err == nil && send == nil
	if err == nil && send != nil {
		if beforeSend != nil {
			beforeSend()
		}
		err = send()
	}
	if err != nil {
		w.rearmIdleStop(run, m.ID)
	}
	return spawned, err
}

// awaitStoppedWorker waits for the last process of member's seated worker to
// end, when the worker is stopped.
//
// 🚨 A stop returns before its process drains. A revival while the old
// process still runs would put two processes on one inbox and one
// conversation. The wait runs before the binding, outside run.workerMu, so
// no other member's reports wait on it. The member's send lock, which the
// caller holds, keeps an idle stop out until the binding is done.
func (w *Workspace) awaitStoppedWorker(run *talkootRun, member string) error {
	h := w.workers()
	if h == nil {
		return nil
	}
	w.talkoot.mu.Lock()
	id := run.seats[member]
	w.talkoot.mu.Unlock()
	if id == "" {
		return nil
	}
	if exists, live := h.state(id); !exists || live {
		return nil
	}
	if !h.exited(id, workerReadyWait) {
		return fmt.Errorf("revive worker %s: its last process has not exited", id)
	}
	return nil
}

// memberSendMu returns member's send lock in run.
func (w *Workspace) memberSendMu(run *talkootRun, member string) *sync.Mutex {
	w.talkoot.mu.Lock()
	defer w.talkoot.mu.Unlock()
	if run.sendMu == nil {
		run.sendMu = map[string]*sync.Mutex{}
	}
	if run.sendMu[member] == nil {
		run.sendMu[member] = &sync.Mutex{}
	}
	return run.sendMu[member]
}

// bindWorker binds member m's worker under run.workerMu: it spawns one with
// the text as its task, or revives or adopts the seated one and opens its
// turn. It returns the send of the text to a seated worker, which the caller
// runs after the lock, or nil after a spawn.
func (w *Workspace) bindWorker(run *talkootRun, m talkoot.Member, text string) (func() error, error) {
	h := w.workers()
	if h == nil {
		return nil, errors.New("the workspace has no swarm to run a worker member")
	}
	run.workerMu.Lock()
	defer run.workerMu.Unlock()
	// A delivery ends the member's idle wait, and a timer that fires now
	// finds its generation old.
	w.talkoot.mu.Lock()
	disarmIdleStopLocked(run, m.ID)
	w.talkoot.mu.Unlock()
	// 🚨 The member the router handed over can predate an update that has
	// already committed. The binding works from the roster as it is now.
	w.talkoot.mu.Lock()
	cur, ok := memberOf(*run.roster.Load(), m.ID)
	id := run.seats[m.ID]
	w.talkoot.mu.Unlock()
	if !ok {
		return nil, errors.New("the member left the roster")
	}
	m = cur
	if m.Driver == talkoot.DriverNative {
		return nil, errors.New("the member runs natively now; the next post reaches its session")
	}
	if err := w.memberUnbound(m); err != nil {
		return nil, err
	}
	req, err := w.workerRequest(run, m, text)
	if err != nil {
		return nil, err
	}
	// A member in a worktree runs in its own lease, and a spawn and a
	// revival both take it.
	if m.Workspace == talkoot.WorkspaceWorktree {
		if req.Dir, err = w.memberLease(run, m.ID); err != nil {
			return nil, err
		}
	}
	if exists, live := h.state(id); id != "" && exists {
		// 🚨 A worker takes a turn only with the limits the roster gives it
		// now. A seat can outlive a retirement whose unbind line failed, and
		// a live worker can outlive the run that bound it. The persisted
		// posture or tools may then be wider than the roster's.
		held, _ := h.holds(id)
		if sameRequest(held, req) {
			return w.sendWorker(run, h, m.ID, id, live, text, req.Dir)
		}
		w.diagf("talkoot %s: member %s's worker %s holds older limits; the member gets a new worker", run.id, m.ID, id)
		if live {
			_ = h.stop(id)
		}
	}
	token := w.nextWorkerRun(run, m.ID)
	id, err = h.spawn(w.ctx, req, w.workerEvents(run, m.ID, token))
	if err != nil {
		return nil, fmt.Errorf("spawn worker: %w", err)
	}
	// 🚨 The seat is recorded under the lock a roster update commits under,
	// against the roster as it is then. An update that committed while the
	// worker started could not see this seat to retire it, so the binding
	// retires it. An update that commits after sees the seat. The seat line
	// goes first, so a restart finds this worker, not a second one.
	w.talkoot.mu.Lock()
	// The worker runs in the member's worktree from here, even if the checks
	// below stop it at once, so a release waits for it.
	if req.Dir != "" {
		holdLeaseLocked(run, m.ID, id)
	}
	if now, ok := memberOf(*run.roster.Load(), m.ID); !ok || !sameWorker(m, now) {
		w.talkoot.mu.Unlock()
		_ = h.stop(id)
		return nil, errors.New("the roster changed while the worker started; the next delivery starts it again")
	}
	if err := run.room.Append(talkoot.Line{Type: talkoot.LineSeat, At: time.Now(), Member: m.ID, Ref: id}); err != nil {
		w.talkoot.mu.Unlock()
		_ = h.stop(id)
		return nil, fmt.Errorf("record the worker's seat: %w", err)
	}
	run.seats[m.ID] = id
	run.workerTurn[m.ID] = token
	oweLocked(run, m.ID, 1)
	w.talkoot.mu.Unlock()
	run.setIdle(m.ID, false)
	return nil, nil
}

// sendWorker sends text to the member's seated worker, and revives it first
// when its process is gone. dir is the member's lease, or empty for a member
// in the home checkout.
func (w *Workspace) sendWorker(run *talkootRun, h workerHost, member, id string, live bool, text, dir string) (func() error, error) {
	w.talkoot.mu.Lock()
	token := run.workerRun[member]
	w.talkoot.mu.Unlock()
	switch {
	case !live:
		token = w.nextWorkerRun(run, member)
		if err := h.resume(w.ctx, id, dir, w.workerEvents(run, member, token)); err != nil {
			return nil, fmt.Errorf("revive worker %s: %w", id, err)
		}
		run.setIdle(member, false)
		// A revived worker is a new process, and it reports its cost from
		// zero again.
		w.talkoot.mu.Lock()
		delete(run.workerCost, id)
		w.talkoot.mu.Unlock()
	case token == 0:
		// A live worker this run never started: the talkoot was loaded again
		// while the swarm kept the worker. Its reports still reach the run
		// that started it, so this run adopts it.
		token = w.nextWorkerRun(run, member)
		total, err := h.adopt(id, w.workerEvents(run, member, token))
		if err != nil {
			return nil, fmt.Errorf("adopt worker %s: %w", id, err)
		}
		// The run that started the worker charged what it spent so far.
		w.talkoot.mu.Lock()
		if run.workerAdopted == nil {
			run.workerAdopted = map[string]bool{}
		}
		run.workerAdopted[member] = true
		if run.workerCost == nil {
			run.workerCost = map[string]float64{}
		}
		run.workerCost[id] = total
		w.talkoot.mu.Unlock()
	}
	// ⚠️ An update that retired the worker after the binding read its seat
	// has removed the seat. The turn is not sent to a retired worker, and a
	// worker this binding revived or adopted stops again.
	w.talkoot.mu.Lock()
	// A revived or adopted worker runs in the member's worktree from here,
	// even if the check below stops it at once, so a release waits for it.
	if dir != "" {
		holdLeaseLocked(run, member, id)
	}
	if run.seats[member] != id {
		w.talkoot.mu.Unlock()
		_ = h.stop(id)
		return nil, errors.New("the roster retired the member's worker; the next delivery starts a new one")
	}
	// A send that joins a turn the process already runs leaves the router's
	// slot to that turn.
	joined := run.workerTurn[member] == token
	run.workerTurn[member] = token
	oweLocked(run, member, 1)
	w.talkoot.mu.Unlock()
	return func() error {
		if err := sendWhenReady(h, id, text); err != nil {
			// The message never reached the worker, so it owes no turn. A
			// send that opened the recorded turn closes it, whatever an
			// older queued turn still owes: the router counts no turn for a
			// failed delivery. A send that joined a running turn leaves it
			// open while the process owes a turn.
			w.talkoot.mu.Lock()
			if run.workerRun[member] == token {
				oweLocked(run, member, -1)
			}
			drained := false
			if run.workerTurn[member] == token {
				switch {
				case !joined:
					delete(run.workerTurn, member)
				case run.workerOwed[member] == 0:
					drained = true
				}
			}
			w.talkoot.mu.Unlock()
			if drained {
				// 🔑 Every turn the process ran has ended, charged as a
				// queued turn, and the router still holds the slot this
				// send kept open. The router call waits on the run's own
				// queue, because this runs inside a dispatch, and a
				// release from here could deliver to this member again
				// under its own send lock.
				run.later(func() { w.freeDrainedWorkerTurn(run, member, token) })
			}
			return err
		}
		return nil
	}, nil
}

// workerRequest is the spawn a worker member gets from its roster entry.
func (w *Workspace) workerRequest(run *talkootRun, m talkoot.Member, text string) (swarm.SpawnRequest, error) {
	req := swarm.SpawnRequest{
		Task:     text,
		Label:    run.id + "-" + m.ID,
		Backend:  m.Driver,
		Approval: m.Posture,
		Tools:    m.Tools,
		Persona:  m.Persona,
		// The talkoot is the worker's dispatching session, so its asks reach
		// the talkoot's inbox and no session's swarm list shows it.
		SessionID: ctrlproto.TalkootAddr(run.id),
		// The swarm leases nothing for a worker member. One in a worktree
		// runs in the lease its member holds, which the binding sets as Dir.
		SharedTree: true,
	}
	// A worker takes the model the roster names. Without one it takes its
	// own default, not terva's, whose id means nothing to another harness.
	if m.Model != "" || m.Tier != "" {
		prov, model, reasoning, err := w.memberModel(m)
		if err != nil {
			return swarm.SpawnRequest{}, err
		}
		req.Provider, req.Model, req.Reasoning = prov, model, reasoning
	}
	return req, nil
}

// sameRequest reports whether a worker spawned with a runs under the limits
// b would give it. The swarm trims each field at the spawn, so both sides are
// trimmed. A tools list compares nil apart from empty. The session compares
// too: it names the approver, and a worker spawned without the talkoot's
// cannot ask anyone. Only whether each runs in its member's lease compares,
// not where, because a reload moves a worker's dir.
func sameRequest(a, b swarm.SpawnRequest) bool {
	eq := func(x, y string) bool { return strings.TrimSpace(x) == strings.TrimSpace(y) }
	return (a.Dir == "") == (b.Dir == "") && eq(a.SessionID, b.SessionID) && eq(a.Backend, b.Backend) && eq(a.Approval, b.Approval) && eq(a.Persona, b.Persona) &&
		eq(a.Provider, b.Provider) && eq(a.Model, b.Model) && eq(a.Reasoning, b.Reasoning) &&
		(a.Tools == nil) == (b.Tools == nil) && slices.Equal(a.Tools, b.Tools)
}

// sendWhenReady sends a user turn, and retries while a revived worker's inbox
// is not yet listening.
func sendWhenReady(h workerHost, id, text string) error {
	deadline := time.Now().Add(workerReadyWait)
	for {
		err := h.send(id, text)
		if !errors.Is(err, swarm.ErrNotReady) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// chargeWorkerLocked returns what worker id spent since its last report, and
// records total as the new baseline. A total below the baseline is a new
// process that counts from zero. The caller holds w.talkoot.mu.
func chargeWorkerLocked(run *talkootRun, id string, total float64) float64 {
	if run.workerCost == nil {
		run.workerCost = map[string]float64{}
	}
	cost := total - run.workerCost[id]
	if cost < 0 {
		cost = total
	}
	run.workerCost[id] = total
	return cost
}

// nextWorkerRun names a new process of the member's worker. Each spawn and
// each revival is one, so a late report from an earlier process, of this
// worker or a retired one, cannot close a turn of the current one.
func (w *Workspace) nextWorkerRun(run *talkootRun, member string) uint64 {
	w.talkoot.mu.Lock()
	defer w.talkoot.mu.Unlock()
	run.workerSeq++
	if run.workerRun == nil {
		run.workerRun = map[string]uint64{}
		run.workerTurn = map[string]uint64{}
	}
	run.workerRun[member] = run.workerSeq
	// A new process runs none of the old one's messages, and reads none of
	// the texts sent to the old one.
	delete(run.workerOwed, member)
	delete(run.workerAdopted, member)
	delete(run.workerReads, member)
	return run.workerSeq
}

// closeWorkerTurn ends the member's open turn when the process token holds
// it, and reports whether it did. A report from another process, or a second
// report of one turn, finds none.
func (w *Workspace) closeWorkerTurn(run *talkootRun, member string, token uint64) bool {
	w.talkoot.mu.Lock()
	defer w.talkoot.mu.Unlock()
	if run.workerTurn[member] != token {
		return false
	}
	delete(run.workerTurn, member)
	return true
}

// oweLocked changes by n what member's current process owes. The caller
// holds w.talkoot.mu.
func oweLocked(run *talkootRun, member string, n int) {
	if run.workerOwed == nil {
		run.workerOwed = map[string]int{}
	}
	run.workerOwed[member] = max(run.workerOwed[member]+n, 0)
	if run.workerOwed[member] == 0 {
		delete(run.workerOwed, member)
	}
}

// idleLocked reports whether member's worker process token is idle: it is
// the current process, this run started it, it owes no turn, and no turn is
// open. The caller holds w.talkoot.mu.
func idleLocked(run *talkootRun, member string, token uint64) bool {
	return run.workerRun[member] == token && !run.workerAdopted[member] &&
		run.workerOwed[member] == 0 && run.workerTurn[member] == 0
}

// endWorkerTurn counts down by paid the texts a turn of process token took,
// when it ended. It reports whether that closed the member's recorded turn,
// and whether the process is current and owed a turn, which a turn that
// paid nothing still charges. With hold, the recorded turn
// stays open while the process still owes a turn, so the member keeps its
// working slot until the last one ends. One hold of w.talkoot.mu covers both,
// so a send cannot land between the count and the close.
func (w *Workspace) endWorkerTurn(run *talkootRun, member string, token uint64, hold bool, paid int) (closed, owed bool) {
	w.talkoot.mu.Lock()
	defer w.talkoot.mu.Unlock()
	if run.workerRun[member] == token && run.workerOwed[member] > 0 {
		oweLocked(run, member, -min(paid, run.workerOwed[member]))
		owed = true
	}
	if run.workerTurn[member] != token || hold && run.workerOwed[member] > 0 {
		return false, owed
	}
	delete(run.workerTurn, member)
	return true, owed
}

// freeDrainedWorkerTurn closes member's recorded turn and frees its working
// slot, when process token still holds the turn and owes nothing. A delivery
// that joined the turn since then leaves it to that delivery's turn end.
func (w *Workspace) freeDrainedWorkerTurn(run *talkootRun, member string, token uint64) {
	err := run.do(func(rt *talkoot.Router) error {
		w.talkoot.mu.Lock()
		drained := run.workerRun[member] == token && run.workerTurn[member] == token && run.workerOwed[member] == 0
		if drained {
			delete(run.workerTurn, member)
		}
		w.talkoot.mu.Unlock()
		if !drained {
			return nil
		}
		return rt.FreeSlot(member)
	})
	if err != nil && !errors.Is(err, ErrTalkootClosed) {
		w.diagf("talkoot %s: worker member %s could not free its working slot: %v", run.id, member, err)
	}
	w.rearmIdleStop(run, member)
}

// workerTurnPerText reports whether the backend a worker member runs on ends
// a turn for each text it takes.
func workerTurnPerText(driver string) bool {
	b, err := worker.Lookup(driver)
	return err == nil && b.TurnPerText
}

// workerIdle reports whether member's worker process token is idle.
func (w *Workspace) workerIdle(run *talkootRun, member string, token uint64) bool {
	w.talkoot.mu.Lock()
	defer w.talkoot.mu.Unlock()
	return idleLocked(run, member, token)
}

// workerEvents reports one process of a member's worker to the router: each
// turn's cost, which frees the member's working slot, and its reply, as a
// note to the coordinator. A process that exits in a turn leaves the turn
// unreported, so the member does not stay working for good.
//
// ⚠️ A worker reports its cost as a running total, kept per worker, so a turn
// costs the difference and a replacement worker starts from zero.
func (w *Workspace) workerEvents(run *talkootRun, member string, token uint64) workerEvents {
	// Wait for a binding in flight to record its turn.
	settle := func() {
		run.workerMu.Lock()
		run.workerMu.Unlock() //nolint:staticcheck // an empty section waits for the holder
	}
	// A process runs one backend for its life: a roster update that changes
	// the driver retires the worker.
	m, _ := memberOf(*run.roster.Load(), member)
	hold := workerTurnPerText(memberDriver(m))
	// A backend that echoes its user turns but folds queued texts into one
	// turn pays what it owes by the texts each turn took, not one a turn.
	folds := !hold && workerReportsReads(memberDriver(m))
	return workerEvents{
		tool: func(callID, name string) {
			// Only the member's current process shows its tools. A stale
			// process's events would draw a call that nothing ends.
			//
			// 🔑 The check and the router call share one hold of
			// w.talkoot.mu, which nextWorkerRun takes to name a new
			// process, so no revival lands between them. The order,
			// run.mu then w.talkoot.mu then the router's own lock, is the
			// one an update takes.
			//
			// ⚠️ This runs on the worker's event path, which must not wait.
			// A busy lock skips the update, as talkootActivity does for a
			// native session. The tool is only a view, and the router
			// clears every call when the turn ends.
			run.tryDo(func(rt *talkoot.Router) error {
				if !w.talkoot.mu.TryLock() {
					return nil
				}
				defer w.talkoot.mu.Unlock()
				if run.workerRun[member] != token {
					return nil
				}
				if name != "" {
					rt.ToolStarted(member, callID, name)
				} else {
					rt.ToolEnded(member, callID)
				}
				return nil
			})
		},
		userTurn: func(text string) {
			if p, ok := w.takeWorkerRead(run, member, token, text); ok {
				// The event path must not wait, and reads must land in
				// order, so the run's own queue takes the router call.
				run.later(func() {
					if err := run.do(func(rt *talkoot.Router) error {
						w.talkoot.mu.Lock()
						defer w.talkoot.mu.Unlock()
						return w.applyWorkerReadLocked(run, rt, member, p.seq, p.r)
					}); err != nil && !errors.Is(err, ErrTalkootClosed) {
						w.diagf("talkoot %s: worker member %s could not report a read: %v", run.id, member, err)
					}
				})
			}
		},
		turnEnd: func(id string, total float64, reply string, texts int) {
			settle()
			// A turn on a backend that folds pays exactly the texts it
			// echoed, none included.
			paid := 1
			if folds {
				paid = texts
			}
			closed, owed := w.endWorkerTurn(run, member, token, hold, paid)
			if !closed {
				// A message sent while a turn ran ends in a turn of its own,
				// which no turn of the run records. On a backend that ends a
				// turn for each text, the recorded turn stays open until the
				// last of them, which frees the slot and leaves the worker
				// idle.
				if owed {
					// It spent as the member, and a revival after an idle
					// stop counts the cost from zero again, so it is charged
					// now or never.
					w.talkoot.mu.Lock()
					cost := chargeWorkerLocked(run, id, total)
					w.talkoot.mu.Unlock()
					if err := run.do(func(rt *talkoot.Router) error {
						w.postWorkerReply(rt, member, reply)
						return rt.QueuedTurnEnded(member, cost)
					}); err != nil {
						w.diagf("talkoot %s: worker member %s could not report a queued turn: %v", run.id, member, err)
					}
					if w.workerIdle(run, member, token) {
						w.armIdleStop(run, member, id, token)
					}
					return
				}
				w.diagf("talkoot %s: dropped a turn end from worker %s, which holds no open turn of member %s", run.id, id, member)
				return
			}
			w.talkoot.mu.Lock()
			cost := chargeWorkerLocked(run, id, total)
			w.talkoot.mu.Unlock()
			if err := run.do(func(rt *talkoot.Router) error {
				w.postWorkerReply(rt, member, reply)
				return rt.TurnEnded(member, cost)
			}); err != nil {
				w.diagf("talkoot %s: worker member %s could not report its turn: %v", run.id, member, err)
			}
			if w.workerIdle(run, member, token) {
				w.armIdleStop(run, member, id, token)
			}
		},
		exit: func(id string, total float64) {
			settle()
			// A process that exited runs nothing it was sent, so no later
			// report of it pays a queued turn.
			w.talkoot.mu.Lock()
			owed := run.workerRun[member] == token && run.workerOwed[member] > 0
			if run.workerRun[member] == token {
				delete(run.workerOwed, member)
				// A text the process never read, it never will. The member
				// keeps its chain, as a ReadDriver's contract says.
				delete(run.workerReads, member)
			}
			w.talkoot.mu.Unlock()
			if !w.closeWorkerTurn(run, member, token) {
				if !owed {
					return
				}
				// It stopped in a queued turn, which holds no working slot.
				// That turn is charged and paused as a recorded one is.
				w.talkoot.mu.Lock()
				retired := run.seats[member] != id
				cost := chargeWorkerLocked(run, id, total)
				w.talkoot.mu.Unlock()
				if err := run.do(func(rt *talkoot.Router) error {
					if retired {
						return rt.QueuedTurnEnded(member, cost)
					}
					return rt.QueuedTurnStopped(member, cost, "the worker stopped before its turn ended")
				}); err != nil {
					w.diagf("talkoot %s: worker member %s could not report its stopped queued turn: %v", run.id, member, err)
				}
				return
			}
			// A worker the roster retired stopped because a person changed
			// its limits. Its turn ends as a native member's does when an
			// update takes its seat, and it spent as the member.
			w.talkoot.mu.Lock()
			retired := run.seats[member] != id
			cost := chargeWorkerLocked(run, id, total)
			w.talkoot.mu.Unlock()
			if err := run.do(func(rt *talkoot.Router) error {
				if retired {
					return rt.TurnEnded(member, cost)
				}
				// It spent until it stopped, and a person looks at why.
				return rt.TurnStopped(member, cost, "the worker stopped before its turn ended")
			}); err != nil {
				w.diagf("talkoot %s: worker member %s could not report its stopped turn: %v", run.id, member, err)
			}
		},
	}
}

// postWorkerReply records a worker's reply as a note to the coordinator. A
// note wakes nobody. The coordinator reads it with its next delivery, and the
// person reads it in the room.
func (w *Workspace) postWorkerReply(rt *talkoot.Router, member, reply string) {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return
	}
	lead := ""
	for _, m := range rt.Members() {
		if m.Role == talkoot.RoleCoordinator {
			lead = m.ID
		}
	}
	if lead == "" || lead == member {
		return
	}
	if len(reply) > talkoot.MaxBodyBytes {
		reply = strings.ToValidUTF8(reply[:talkoot.MaxBodyBytes-len(" …")], "") + " …"
	}
	if _, err := rt.Send(member, talkoot.Outgoing{To: []string{lead}, Kind: talkoot.KindNote, Body: reply}); err != nil {
		w.diagf("talkoot: the reply of worker member %s did not reach the room: %v", member, err)
	}
}
