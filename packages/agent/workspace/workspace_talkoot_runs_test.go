package workspace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/provider"
)

// runScript is a provider that tells the request of a routine's run from
// every other request. A run's text holds runMark, and no other text does.
type runScript struct {
	mu sync.Mutex
	// reply is the run's last text.
	reply string
	// fail refuses the run's request.
	fail bool
	// hold, when it is set, keeps the run's request open until it closes.
	hold    chan struct{}
	entered chan struct{}
	// send holds the arguments of the talkoot_send call that a member makes
	// when it reads a run's report. Empty makes no call.
	send string
	// memberAsk holds the arguments of the ask_user_question call that a
	// member makes when it reads a text with memberAskOn in it. An empty
	// memberAskOn is the mark of a run's report.
	memberAsk, memberAskOn string
	// memberThen and memberThenArgs make the member call one more tool
	// after it read that nobody answered its question.
	memberThen, memberThenArgs string
	// memberBash holds the arguments of the bash call that a member makes
	// when it reads a text with memberBashOn in it, one call for each time
	// that the text arrives.
	memberBash, memberBashOn string
	// memberTool, when it is set, is the tool of that call in place of bash.
	memberTool string
	// seq holds the commands that a run gives to bash, one after each
	// result, before its last text.
	seq []string
	// others holds each request that is no run's.
	others []string
	// bodies holds the request of each run, and offered the tools that the
	// first of them offered.
	bodies  []string
	offered []string
	// usage, when it is above zero, is the count of prompt tokens that each
	// response to a run reports. On a model with a price it is a cost.
	usage int
	// callName and callArgs make a run call one tool before its last text.
	// stall keeps the request after that call open until the turn is
	// cancelled. again makes the run call the tool once more after each
	// result, in place of its last text.
	callName, callArgs string
	stall, again       bool
	// cut makes each response to a run report its usage and then break off,
	// so the request fails after it has a cost.
	cut bool
	// tools holds the tools that each first request of a run offered.
	tools [][]string
	// onRun, when it is set, is called one time, at the first request of a
	// run and before its response.
	onRun func()
}

const (
	// The first text of every run holds these words, and no other text
	// does: a run at the routine's time, at a follow-up, and at an event.
	runMark    = "terva started this run "
	reportMark = "report of a run of your routine"
)

func (p *runScript) serve(rw http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	body := string(raw)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if !strings.Contains(body, runMark) {
		p.mu.Lock()
		send, ask, askOn, then, thenArgs := p.send, p.memberAsk, p.memberAskOn, p.memberThen, p.memberThenArgs
		bash, bashOn, bashTool := p.memberBash, p.memberBashOn, p.memberTool
		if bashTool == "" {
			bashTool = "bash"
		}
		p.others = append(p.others, body)
		p.mu.Unlock()
		if askOn == "" {
			askOn = reportMark
		}
		noTool := !strings.Contains(body, `"role":"tool"`)
		switch {
		case bash != "" && askedSince(body, bashOn):
			// toolCallProvider makes no call once the request holds a
			// result, so this call is written here.
			rw.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(rw, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_b%d\",\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":%s}}]},\"finish_reason\":null}]}\n\n", strings.Count(body, `"role":"tool"`), bashTool, strconv.Quote(bash))
			fmt.Fprint(rw, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
			fmt.Fprint(rw, "data: [DONE]\n\n")
		case send != "" && noTool && strings.Contains(body, reportMark):
			toolCallProvider("talkoot_send", send)(rw, r)
		case ask != "" && noTool && strings.Contains(body, askOn):
			toolCallProvider("ask_user_question", ask)(rw, r)
		case then != "" && strings.Count(body, `"role":"tool"`) == 1 && strings.Contains(body, "no person answered in"):
			rw.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(rw, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_2\",\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":%s}}]},\"finish_reason\":null}]}\n\n", then, strconv.Quote(thenArgs))
			fmt.Fprint(rw, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
			fmt.Fprint(rw, "data: [DONE]\n\n")
		default:
			okProvider(rw, r)
		}
		return
	}
	afterCall := strings.Contains(body, `"role":"tool"`)
	p.mu.Lock()
	if !afterCall {
		var req struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.Unmarshal(raw, &req)
		var names []string
		for _, tool := range req.Tools {
			names = append(names, tool.Function.Name)
		}
		if p.bodies == nil {
			p.offered = names
		}
		p.tools = append(p.tools, names)
	}
	p.bodies = append(p.bodies, body)
	onRun := p.onRun
	p.onRun = nil
	reply, fail, hold := p.reply, p.fail, p.hold
	usage, callName, callArgs, stall, cut, again := p.usage, p.callName, p.callArgs, p.stall, p.cut, p.again
	if n := strings.Count(body, `"role":"tool"`); n < len(p.seq) {
		// The next command of the list. The run goes on after each result.
		b, _ := json.Marshal(map[string]string{"command": p.seq[n]})
		callName, callArgs, again = "bash", string(b), true
	}
	p.mu.Unlock()
	if onRun != nil {
		onRun()
	}
	// A run that calls a tool waits after the call, so the call is done.
	if hold != nil && (callName == "" || afterCall) {
		select {
		case p.entered <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
			return
		case <-hold:
		}
	}
	if fail {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusBadRequest)
		_, _ = rw.Write([]byte(`{"error":{"message":"refused"}}`))
		return
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	spent := func() {
		if usage > 0 {
			fmt.Fprintf(rw, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":0,\"total_tokens\":%d}}\n\n", usage, usage)
		}
	}
	switch {
	case cut:
		spent()
		if f, ok := rw.(http.Flusher); ok {
			f.Flush()
		}
		// The connection ends with no last chunk, as a server that fell
		// over does.
		panic(http.ErrAbortHandler)
	case callName != "" && (!afterCall || again):
		fmt.Fprintf(rw, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":%s}}]},\"finish_reason\":null}]}\n\n", callName, strconv.Quote(callArgs))
		fmt.Fprint(rw, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		spent()
		fmt.Fprint(rw, "data: [DONE]\n\n")
		return
	case stall && afterCall:
		<-r.Context().Done()
		return
	}
	fmt.Fprintf(rw, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%s},\"finish_reason\":null}]}\n\n", strconv.Quote(reply))
	fmt.Fprint(rw, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	spent()
	fmt.Fprint(rw, "data: [DONE]\n\n")
}

// askedSince reports whether body holds mark with no tool result after it.
// A member's session keeps the results of its earlier turns, so "no result
// in the request" holds for its first call alone.
func askedSince(body, mark string) bool {
	i := strings.LastIndex(body, mark)
	return i >= 0 && !strings.Contains(body[i:], `"role":"tool"`)
}

func (p *runScript) set(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn()
}

// rest returns each request that was no run's.
func (p *runScript) rest() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.others)
}

func (p *runScript) runs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.bodies)
}

// held makes each run's request wait, and returns the call that lets them go.
// It runs at cleanup too, before the workspace closes, because the server's
// Close waits for an open request.
func (p *runScript) held(t *testing.T) (release func()) {
	t.Helper()
	p.hold, p.entered = make(chan struct{}), make(chan struct{}, 8)
	var once sync.Once
	release = func() { once.Do(func() { close(p.hold) }) }
	t.Cleanup(release)
	return release
}

// runCrew is the crew talkoot on a daemon with routines on, over the
// provider p, after one turn of helm's. held says that a run's request waits.
func runCrew(t *testing.T, p *runScript, held bool) (w *Workspace, seat talkootSeat, clock *stepClock, cwd string, release func()) {
	t.Helper()
	cwd = talkootHome(t)
	routinesConfig(t, `{"max": 5}`)
	clock = &stepClock{now: time.Now()}
	w = wakeDaemon(t, cwd, clock, p.serve)
	release = func() {}
	if held {
		// After the daemon, so it runs before the daemon closes.
		release = p.held(t)
	}
	if _, err := w.talkootCreate(t.Context(), "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	return w, helmAfterOneTurn(t, w), clock, cwd, release
}

// seatAfterOneTurn gives member one turn, so it holds a seat.
func seatAfterOneTurn(t *testing.T, w *Workspace, member string) talkootSeat {
	t.Helper()
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{member}, "Watch the CI run of the import.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, member+"'s first turn", func() bool {
		m := memberView(t, w, "crew", member)
		return m.Status.Turns == 1 && !m.Status.Working
	})
	seat, ok := w.talkootSeatOf(memberView(t, w, "crew", member).Session)
	if !ok {
		t.Fatalf("%s holds no seat", member)
	}
	return seat
}

// fire moves the clock and lets the timers fire. It returns once the team
// has decided what each firing does. A run that began may still be at work.
func fire(t *testing.T, w *Workspace, clock *stepClock, d time.Duration) {
	t.Helper()
	clock.advance(d)
	w.Timers().Poll()
	settleRuns(t, w)
}

func settleRuns(t *testing.T, w *Workspace) {
	t.Helper()
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	run.later(func() { close(done) })
	// ⚠️ The wait is long on purpose. A firing that tries a start several
	// times writes the roster and the room at each try. On 2026-11-18 a
	// gate run on a machine with a load of 18 took over 10 seconds for one
	// such firing, which takes a tenth of a second alone. The firing was
	// still at work when the old wait of 10 seconds ended.
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("the team did not finish the firing")
	}
}

func runRoots(t *testing.T) []talkoot.Line {
	t.Helper()
	return roomLines(t, "crew", func(l talkoot.Line) bool {
		return l.Type == talkoot.LineEnvelope && l.Envelope != nil && l.Envelope.Kind == talkoot.KindRun
	})
}

// runEnds lists what the routines sent to their members: reports and notes.
func runEnds(t *testing.T, kind talkoot.Kind) []talkoot.Line {
	t.Helper()
	return roomLines(t, "crew", func(l talkoot.Line) bool {
		return l.Type == talkoot.LineEnvelope && l.Envelope != nil && l.Envelope.Kind == kind && talkoot.IsRoutineActor(l.Envelope.From)
	})
}

// waitRunEnds waits until the room holds the turn line of n runs, and until
// the router has written what follows the last of them.
//
// ⚠️ The router writes the turn line of a run and then its report or its
// note, under one lock. A test that reads the room between the two finds a
// run with no end. A Docs Gate run on 2026-11-18 failed that way, in a test
// that passes alone. A call that takes the router's lock returns after both
// lines are in the room.
func waitRunEnds(t *testing.T, w *Workspace, what string, n int) {
	t.Helper()
	waitTalkoot(t, what, func() bool { return len(runTurns(t)) == n })
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	if err := run.do(func(rt *talkoot.Router) error { rt.Unattended(""); return nil }); err != nil {
		t.Fatal(err)
	}
}

func runTurns(t *testing.T) []talkoot.Line {
	t.Helper()
	return roomLines(t, "crew", func(l talkoot.Line) bool {
		return l.Type == talkoot.LineTurn && strings.HasPrefix(l.Ref, talkoot.RoutinePrefix)
	})
}

func routineSteps(t *testing.T, action string) []talkoot.Line {
	t.Helper()
	var out []talkoot.Line
	for _, l := range routineLines(t) {
		if l.Action == action {
			out = append(out, l)
		}
	}
	return out
}

func routineOf(t *testing.T, w *Workspace, rid string) ctrlproto.TalkootRoutine {
	t.Helper()
	v := routinesView(t, w)
	if v != nil {
		for _, r := range v.Routines {
			if r.ID == rid {
				return r
			}
		}
	}
	t.Fatalf("the view holds no routine %s: %+v", rid, v)
	return ctrlproto.TalkootRoutine{}
}

// sessionSays joins the text of every message in the live session sid.
func sessionSays(t *testing.T, w *Workspace, sid string) string {
	t.Helper()
	s := w.existing(sid)
	if s == nil {
		t.Fatalf("session %s is not live", sid)
	}
	var b strings.Builder
	for _, m := range s.agent.Messages() {
		for _, c := range m.Content {
			if tb, ok := c.(provider.TextBlock); ok {
				b.WriteString(tb.Text + "\n")
			}
		}
	}
	return b.String()
}

// The whole path (decision 0029 rules 8 and 9). A routine comes due, a run
// starts in a session of its own with the run at the root of a new chain,
// and the run ends in a report to the member that owns the routine. That
// member decides what to post, and the coordinator reads only that.
func TestARoutineThatComesDueStartsARunAndItsMemberDecidesWhatToPost(t *testing.T) {
	p := &runScript{
		reply: "CI is red on #12. The detail is in the note jev/ci.",
		send:  `{"to":["helm"],"kind":"message","body":"CI is red on pull request 12."}`,
	}
	w, _, clock, _, _ := runCrew(t, p, false)
	jev := seatAfterOneTurn(t, w, "jev")
	rid := approved(t, w, jev, ciTask)
	helmSess, jevSess := memberView(t, w, "crew", "helm").Session, memberView(t, w, "crew", "jev").Session

	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "jev to read the report and to tell helm", func() bool {
		h, j := memberView(t, w, "crew", "helm"), memberView(t, w, "crew", "jev")
		return h.Status.Turns == 2 && !h.Status.Working && j.Status.Turns == 3 && !j.Status.Working
	})

	// The root: the run, from the routine, to nobody.
	roots := runRoots(t)
	if len(roots) != 1 {
		t.Fatalf("one firing must start one run, got %d roots", len(roots))
	}
	root := roots[0]
	if e := root.Envelope; e.From != talkoot.RoutinePrefix+rid || len(e.To) != 0 || e.Chain.Root != e.ID || e.Chain.Hops != 0 || e.Body != ciTask.Prompt {
		t.Fatalf("the root of the chain must be the run, with the routine's prompt: %+v", e)
	}
	if root.Member != "jev" || root.By != "human:sothr" {
		t.Errorf("the root must name the routine's member and the person who approved it: %+v", root)
	}
	// 🚨 The run is a session of its own. A run in the member's session
	// would lengthen the conversation that the person reads.
	runSess := root.Ref
	if runSess == "" || runSess == jevSess || runSess == helmSess {
		t.Fatalf("the run must have a session of its own, got %q (jev %q, helm %q)", runSess, jevSess, helmSess)
	}
	if _, err := os.Stat(w.sessionPath(runSess)); err != nil {
		t.Errorf("the run's session must stay on disk, where a person can read it: %v", err)
	}
	if !w.isTalkootMember(runSess) {
		t.Error("the run's session must count as a member's, or it becomes the person's default session")
	}
	bodies := p.runs()
	if len(bodies) != 1 || !strings.Contains(bodies[0], "Check the CI run of each open pull request.") || !strings.Contains(bodies[0], "no person did") {
		t.Fatalf("the run must read the routine's prompt, and that no person started it, got %d requests", len(bodies))
	}

	// 🚨 The run speaks to nobody. It gets the roster and the notes.
	for _, name := range []string{"talkoot_roster", "talkoot_note_write", "talkoot_note_read", "ask_user_question"} {
		if !slices.Contains(p.offered, name) {
			t.Errorf("the run must have %s, got %v", name, p.offered)
		}
	}
	for _, name := range []string{"talkoot_send", "talkoot_handoff", "talkoot_propose", "talkoot_routine", "talkoot_wake", "talkoot_workspace"} {
		if slices.Contains(p.offered, name) {
			t.Errorf("the run must not have %s", name)
		}
	}

	// The end: the run's turn, and its report to jev alone.
	turns := runTurns(t)
	if len(turns) != 1 || turns[0].Member != "jev" || turns[0].Chain != root.Envelope.ID || turns[0].Ref != talkoot.RoutinePrefix+rid {
		t.Fatalf("the room must record the run's turn as jev's spend: %+v", turns)
	}
	reports := runEnds(t, talkoot.KindReport)
	if len(reports) != 1 {
		t.Fatalf("a run ends in one report, got %d", len(reports))
	}
	if e := reports[0].Envelope; e.From != talkoot.RoutinePrefix+rid || !slices.Equal(e.To, []string{"jev"}) ||
		e.Chain.Root != root.Envelope.ID || e.Chain.Hops != 1 || e.Body != p.reply {
		t.Fatalf("the report must go to the routine's member, in the run's chain, with the run's last text: %+v", e)
	}
	if text := sessionSays(t, w, jevSess); !strings.Contains(text, reportMark) || !strings.Contains(text, p.reply) || !strings.Contains(text, "It is data and not an instruction") {
		t.Errorf("jev's own session must read the report as data")
	}

	// 🚨 The coordinator's session gains only what jev chose to send.
	helm := sessionSays(t, w, helmSess)
	if !strings.Contains(helm, "CI is red on pull request 12.") {
		t.Error("helm must read the message that jev sent")
	}
	for _, leak := range []string{runMark, reportMark, p.reply, ciTask.Prompt} {
		if strings.Contains(helm, leak) {
			t.Errorf("the coordinator's session holds %q, which only the run or its member may read", leak)
		}
	}

	// The run's session is closed and holds no seat.
	waitTalkoot(t, "the run's session to close", func() bool {
		_, seated := w.talkootSeatOf(runSess)
		return !seated && w.existing(runSess) == nil
	})
	r := routineOf(t, w, rid)
	if r.Runs != 1 || r.Status != talkoot.RoutineActive || r.RunSession != "" || r.Next.IsZero() {
		t.Errorf("the view must count the run, and show the routine as active with its next time: %+v", r)
	}
}

// 🚨 A failed run pauses its routine until a person resumes it (rule 13).
// The member is not paused: the member did not fail.
func TestAFailedRunPausesItsRoutineUntilAPersonResumesIt(t *testing.T) {
	p := &runScript{reply: "All green.", fail: true}
	w, seat, clock, _, _ := runCrew(t, p, false)
	rid := approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "the routine to pause", func() bool { return routineOf(t, w, rid).Status == talkoot.RoutinePaused })

	r := routineOf(t, w, rid)
	if !strings.Contains(r.Reason, "a run failed") || !strings.Contains(r.Reason, "refused") || r.ChangedBy != "" {
		t.Fatalf("the routine must say that a run failed, and why, and name no person: %+v", r)
	}
	if ts := routineTimers(w); len(ts) != 0 {
		t.Fatalf("a paused routine must hold no timer: %+v", ts)
	}
	paused := routineSteps(t, talkoot.ActionPaused)
	if len(paused) != 1 || paused[0].Ref != rid || paused[0].By != "" || !strings.Contains(paused[0].Reason, "refused") {
		t.Fatalf("the room must say that the routine paused, and why: %+v", paused)
	}
	if turns := runTurns(t); len(turns) != 1 || turns[0].Guard != talkoot.GuardFailed {
		t.Fatalf("the run's turn line must say that it failed: %+v", turns)
	}
	if len(runEnds(t, talkoot.KindReport)) != 0 {
		t.Error("a failed run sends no report")
	}
	if notes := runEnds(t, talkoot.KindNote); len(notes) != 1 || !strings.Contains(notes[0].Envelope.Body, "The run failed, and it wrote no report. The cause: ") {
		t.Fatalf("the member must get a note about the failed run: %+v", notes)
	}
	// 🚨 The member did not fail, and a failed run is no cause for a turn.
	// The run counts as one of helm's turns of the day, and nothing else ran.
	if m := memberView(t, w, "crew", "helm"); m.Status.Paused != "" || m.Status.Turns != 2 {
		t.Fatalf("a failed run must not pause its member or start a turn of it: %+v", m.Status)
	}

	// It stays paused: no time of it starts a run.
	fire(t, w, clock, 16*time.Minute)
	if n := len(runRoots(t)); n != 1 {
		t.Fatalf("a paused routine must start no run, got %d roots", n)
	}

	// A person resumes it, and the next time runs.
	p.set(func() { p.fail = false })
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineResume, "the provider is back"); err != nil {
		t.Fatal(err)
	}
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "the report of the run after the resume", func() bool { return len(runEnds(t, talkoot.KindReport)) == 1 })
}

// A missed time follows the card (rule 7). A routine set to skip starts no
// run after downtime. One set to catch up runs one time, however many times
// it missed, and the run is told how long the daemon was away.
func TestAMissedTimeFollowsTheCard(t *testing.T) {
	p := &runScript{reply: "Done."}
	w, seat, clock, cwd, _ := runCrew(t, p, false)
	skip := ciTask
	skip.Prompt, skip.CatchUp = "Skip a missed time.", new(false)
	skipID := approved(t, w, seat, skip)
	catch := ciTask
	catch.Prompt = "Catch a missed time up."
	catchID := approved(t, w, seat, catch)
	_ = w.Close()

	// Two hours pass with no daemon: eight times of each routine. The
	// minute puts the eighth time behind the clock, whatever instant the
	// approval took as its start.
	clock.advance(2*time.Hour + time.Minute)
	w2 := wakeDaemon(t, cwd, clock, p.serve)
	w2.Timers().Poll()
	settleRuns(t, w2)
	waitTalkoot(t, "the report of the one run that catches up", func() bool { return len(runEnds(t, talkoot.KindReport)) == 1 })

	roots := runRoots(t)
	if len(roots) != 1 || roots[0].Envelope.From != talkoot.RoutinePrefix+catchID {
		t.Fatalf("only the routine that catches up runs, and it runs one time for eight missed times: %+v", roots)
	}
	bodies := p.runs()
	if len(bodies) != 1 || !strings.Contains(bodies[0], "This run is late") || !strings.Contains(bodies[0], "missed 7 earlier time(s)") {
		t.Fatalf("the run must be told that it is late, and how many times the daemon missed, got %d requests", len(bodies))
	}
	skipped := routineSteps(t, talkoot.RoutineSkipped)
	if len(skipped) != 1 || skipped[0].Ref != skipID || !strings.Contains(skipped[0].Reason, "skips a time that it missed") {
		t.Fatalf("the room must say that the other routine skipped its missed time: %+v", skipped)
	}
	if r := routineOf(t, w2, skipID); r.Runs != 0 || r.Status != talkoot.RoutineActive {
		t.Errorf("a skipped time counts no run and leaves the routine active: %+v", r)
	}

	// The next time is on time, and the routine that skipped runs it.
	fire(t, w2, clock, 16*time.Minute)
	waitTalkoot(t, "both routines to run at their next time", func() bool { return len(runEnds(t, talkoot.KindReport)) == 3 })
	if last := p.runs(); strings.Contains(last[1], "This run is late") || strings.Contains(last[2], "This run is late") {
		t.Error("a run at its time must not be told that it is late")
	}
}

// 🚨 The policy is read at each firing. A person who turned routines off
// gets no run from a routine that was approved before.
func TestARoutineStartsNoRunWhileRoutinesAreOff(t *testing.T) {
	p := &runScript{reply: "Done."}
	w, seat, clock, _, _ := runCrew(t, p, false)
	rid := approved(t, w, seat, ciTask)
	routinesConfig(t, "")
	fire(t, w, clock, 16*time.Minute)
	if n := len(runRoots(t)); n != 0 {
		t.Fatalf("routines are off, and %d runs started", n)
	}
	if ts := routineTimers(w); len(ts) != 1 || ts[0].ID != rid {
		t.Fatalf("the timer must stay, for the day the person turns routines on again: %+v", ts)
	}
	routinesConfig(t, `{"max": 5}`)
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "a run once routines are on again", func() bool { return len(runEnds(t, talkoot.KindReport)) == 1 })
}

// 🚨 The routine's record decides, and not its timer. A pause or a delete
// that could not remove the timer must still stop the next run.
func TestAPausedOrDeletedRoutineStartsNoRunWhateverItsTimerHolds(t *testing.T) {
	p := &runScript{reply: "Done."}
	w, seat, clock, _, _ := runCrew(t, p, false)
	paused := approved(t, w, seat, ciTask)
	deleted := approved(t, w, seat, ciTask)
	restored := approved(t, w, seat, ciTask)
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	// Each timer as a failed removal would leave it.
	var kept []talkoot.Routine
	for _, rid := range []string{paused, deleted, restored} {
		r, err := talkoot.LoadRoutine(run.dir, rid)
		if err != nil {
			t.Fatal(err)
		}
		kept = append(kept, r)
	}
	if _, err := setRoutine(t, w, paused, ctrlproto.TalkootRoutinePause, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := setRoutine(t, w, deleted, ctrlproto.TalkootRoutineDelete, ""); err != nil {
		t.Fatal(err)
	}
	// 🚨 The third one is deleted, and its record came back as it was: from
	// a backup, or from a stray write. The record says active. The room
	// says that a person deleted it, and the room decides.
	if _, err := setRoutine(t, w, restored, ctrlproto.TalkootRoutineDelete, ""); err != nil {
		t.Fatal(err)
	}
	if err := talkoot.SaveRoutine(run.dir, kept[2]); err != nil {
		t.Fatal(err)
	}
	for _, r := range kept {
		sched, _, err := routineSchedule(r.Schedule, clock.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := w.routineTimer(w.Timers(), "crew", r, sched); err != nil {
			t.Fatal(err)
		}
	}
	if ts := routineTimers(w); len(ts) != 3 {
		t.Fatalf("fixture: the three timers must be set again, got %+v", ts)
	}
	fire(t, w, clock, 16*time.Minute)
	for _, l := range runRoots(t) {
		for name, rid := range map[string]string{"paused": paused, "deleted": deleted, "deleted and restored": restored} {
			if l.Envelope.From == talkoot.RoutinePrefix+rid {
				t.Errorf("the %s routine started a run", name)
			}
		}
	}
	// The firing ran, and it removed what it found.
	if ts := routineTimers(w); len(ts) != 0 {
		t.Fatalf("a firing must remove the timer of a routine that may not run: %+v", ts)
	}
}

// 🚨 A pause on the member holds its runs. A run spends as its member, so a
// person or a cap that paused the member must stop its routines too. The
// room says so one time, and not at each quarter hour.
func TestAPauseOnTheMemberHoldsItsRuns(t *testing.T) {
	p := &runScript{reply: "Done."}
	w, seat, clock, _, _ := runCrew(t, p, false)
	rid := approved(t, w, seat, ciTask)
	if err := w.talkootPause(t.Context(), "crew", "sothr", "helm", "", "over budget"); err != nil {
		t.Fatal(err)
	}
	fire(t, w, clock, 16*time.Minute)
	fire(t, w, clock, 16*time.Minute)
	if n := len(runRoots(t)); n != 0 {
		t.Fatalf("a paused member started %d runs", n)
	}
	skipped := routineSteps(t, talkoot.RoutineSkipped)
	if len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "a pause holds it") || !strings.Contains(skipped[0].Reason, "over budget") {
		t.Fatalf("two held times with one cause must write one line, with the pause's reason: %+v", skipped)
	}
	if r := routineOf(t, w, rid); r.Runs != 0 || r.Status != talkoot.RoutineActive {
		t.Fatalf("a held time counts no run and leaves the routine active: %+v", r)
	}
	if err := w.talkootResume(t.Context(), "crew", "sothr", "helm", ""); err != nil {
		t.Fatal(err)
	}
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "a run after the resume", func() bool { return len(runEnds(t, talkoot.KindReport)) == 1 })
	// A run came between, so the same cause is new again.
	if err := w.talkootPause(t.Context(), "crew", "sothr", "helm", "", "over budget"); err != nil {
		t.Fatal(err)
	}
	fire(t, w, clock, 16*time.Minute)
	if n := len(routineSteps(t, talkoot.RoutineSkipped)); n != 2 {
		t.Fatalf("a held time after a run must write its own line, got %d lines", n)
	}
}

// A routine with an end stops at it. The run that reaches the count is the
// last one, the routine gets the status ended, and it does not resume.
func TestARoutineEndsAfterItsRuns(t *testing.T) {
	p := &runScript{reply: "Done."}
	w, seat, clock, _, _ := runCrew(t, p, false)
	ask := ciTask
	ask.EndAfterRuns = new(1)
	rid := approved(t, w, seat, ask)
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "the last run's report", func() bool { return len(runEnds(t, talkoot.KindReport)) == 1 })
	r := routineOf(t, w, rid)
	if r.Status != talkoot.RoutineEnded || r.Runs != 1 || !strings.Contains(r.Reason, "1 of its 1 runs") {
		t.Fatalf("the routine must end at its count, and say why: %+v", r)
	}
	if ts := routineTimers(w); len(ts) != 0 {
		t.Fatalf("an ended routine must hold no timer: %+v", ts)
	}
	if ended := routineSteps(t, talkoot.RoutineEnded); len(ended) != 1 || ended[0].Ref != rid {
		t.Fatalf("the room must say that the routine ended: %+v", ended)
	}
	// 🚨 A pause and a resume must not give it a timer again.
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutinePause, ""); err == nil || !strings.Contains(err.Error(), "reached its end") {
		t.Fatalf("an ended routine must not pause, got %v", err)
	}
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineResume, ""); err == nil {
		t.Fatal("an ended routine must not resume")
	}
	if ts := routineTimers(w); len(ts) != 0 {
		t.Fatalf("a refused pause or resume set a timer: %+v", ts)
	}
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineDelete, ""); err != nil {
		t.Fatalf("a person deletes an ended routine: %v", err)
	}
}

// 🚨 A routine whose count is used up gets no timer from a resume, though a
// hand set its record back to paused.
func TestARoutineThatUsedItsRunsDoesNotResume(t *testing.T) {
	p := &runScript{reply: "Done."}
	w, seat, clock, _, _ := runCrew(t, p, false)
	ask := ciTask
	ask.EndAfterRuns = new(1)
	rid := approved(t, w, seat, ask)
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "the last run's report", func() bool { return len(runEnds(t, talkoot.KindReport)) == 1 })
	editRoutine(t, rid, func(m map[string]any) { m["status"] = talkoot.RoutinePaused })
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineResume, ""); err == nil || !strings.Contains(err.Error(), "reached the end") {
		t.Fatalf("a routine that used its runs must not resume, got %v", err)
	}
	if ts := routineTimers(w); len(ts) != 0 {
		t.Fatalf("the refused resume set a timer: %+v", ts)
	}
}

// A routine past its last day ends at its next time, and starts no run.
func TestARoutinePastItsLastDayEndsAndStartsNoRun(t *testing.T) {
	p := &runScript{reply: "Done."}
	w, seat, clock, _, _ := runCrew(t, p, false)
	ask := ciTask
	ask.EndAfterDays = new(1)
	rid := approved(t, w, seat, ask)
	fire(t, w, clock, 25*time.Hour)
	if n := len(runRoots(t)); n != 0 {
		t.Fatalf("a routine past its last day started %d runs", n)
	}
	if r := routineOf(t, w, rid); r.Status != talkoot.RoutineEnded || !strings.Contains(r.Reason, "its last day was") {
		t.Fatalf("the routine must end, and say why: %+v", r)
	}
	if ended := routineSteps(t, talkoot.RoutineEnded); len(ended) != 1 || len(routineTimers(w)) != 0 {
		t.Fatalf("the room must say that the routine ended, and its timer must go: %+v", ended)
	}
}

// 🚨 One run of a routine at a time. While a run works, the member's own
// session is free, the run's seat sends nothing, and the next time of the
// routine starts no second run.
func TestARunAtWorkHoldsItsRoutineAndItsSeatSendsNothing(t *testing.T) {
	p := &runScript{reply: "Done."}
	w, seat, clock, _, release := runCrew(t, p, true)
	rid := approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	select {
	case <-p.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the run made no request")
	}
	r := routineOf(t, w, rid)
	if r.RunSession == "" || r.Runs != 1 {
		t.Fatalf("the view must show the run that is at work: %+v", r)
	}
	// The run is no turn of the member's.
	if m := memberView(t, w, "crew", "helm"); m.Status.Working || m.Status.Turns != 1 {
		t.Fatalf("a run must not make its member busy: %+v", m.Status)
	}
	runSeat, ok := w.talkootSeatOf(r.RunSession)
	if !ok || runSeat.b.routine == nil {
		t.Fatal("the run's session holds no run seat")
	}
	if _, err := runSeat.Send(talkoot.Outgoing{To: []string{"jev"}, Kind: talkoot.KindMessage, Body: "Hello."}); !errors.Is(err, errRunSeat) {
		t.Errorf("a run must not send, got %v", err)
	}
	// This daemon has wakes off, so the run sets no follow-up either.
	if _, err := runSeat.Wake(10*time.Minute, "Look again."); !errors.Is(err, errWakesOff) {
		t.Errorf("a run must set no follow-up while wakes are off, got %v", err)
	}
	if _, err := runSeat.ProposeRoutine(ciTask, "More."); !errors.Is(err, errRunSeat) {
		t.Errorf("a run must not ask for a routine, got %v", err)
	}
	if _, err := runSeat.Propose([]talkoot.Op{{Op: talkoot.OpRemove, Member: "jev"}}, "", "Less."); !errors.Is(err, errRunSeat) {
		t.Errorf("a run must not propose a roster change, got %v", err)
	}
	if list, err := runSeat.Roster(); err != nil || len(list) != 2 {
		t.Errorf("a run reads the roster: %v %+v", err, list)
	}

	fire(t, w, clock, 16*time.Minute)
	if n := len(runRoots(t)); n != 1 {
		t.Fatalf("a routine whose run is at work started a second run: %d roots", n)
	}
	skipped := routineSteps(t, talkoot.RoutineSkipped)
	if len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "still at work") {
		t.Fatalf("the room must say why the time started no run: %+v", skipped)
	}
	if r := routineOf(t, w, rid); r.Runs != 1 {
		t.Fatalf("a time that started no run must count none: %+v", r)
	}
	release()
	waitTalkoot(t, "the report", func() bool { return len(runEnds(t, talkoot.KindReport)) == 1 })
}

// 🚨 A run has a time limit. A run that waits for a person waits with nobody
// present, so a run past the limit is stopped, and it counts as failed.
func TestARunPastItsLimitIsStoppedAndPausesItsRoutine(t *testing.T) {
	defer func(d, g time.Duration) { routineRunLimit, routineRunGrace = d, g }(routineRunLimit, routineRunGrace)
	routineRunLimit, routineRunGrace = 100*time.Millisecond, time.Hour
	p := &runScript{reply: "Done."}
	w, seat, clock, _, _ := runCrew(t, p, true)
	rid := approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "the routine to pause", func() bool { return routineOf(t, w, rid).Status == talkoot.RoutinePaused })
	if r := routineOf(t, w, rid); !strings.Contains(r.Reason, "passed its limit") {
		t.Fatalf("the routine must say that its run passed the limit: %+v", r)
	}
	if turns := runTurns(t); len(turns) != 1 || turns[0].Guard != talkoot.GuardFailed || turns[0].Interrupted {
		t.Fatalf("the run's turn line must say that it failed: %+v", turns)
	}
	if len(runEnds(t, talkoot.KindReport)) != 0 {
		t.Error("a stopped run sends no report")
	}
}

// 🚨 A run takes its posture and its tools from the roster when it starts. A
// roster update that changes the member stops its run, and that is no
// failure of the routine.
func TestARosterChangeToTheMemberStopsItsRun(t *testing.T) {
	p := &runScript{reply: "Done."}
	w, seat, clock, cwd, _ := runCrew(t, p, true)
	rid := approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	select {
	case <-p.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the run made no request")
	}
	next := strings.Replace(string(crewText(cwd)), "role: coordinator\n", "role: coordinator\n    posture: ask\n", 1)
	if _, err := w.talkootUpdate(t.Context(), "crew", "sothr", []byte(next)); err != nil {
		t.Fatal(err)
	}
	waitRunEnds(t, w, "the run to end", 1)
	if tl := runTurns(t)[0]; !tl.Interrupted || tl.Guard != "" {
		t.Fatalf("a run that a roster update stopped is interrupted, and it did not fail: %+v", tl)
	}
	if len(runEnds(t, talkoot.KindReport)) != 0 || len(runEnds(t, talkoot.KindNote)) != 0 {
		t.Error("an interrupted run sends nothing")
	}
	if r := routineOf(t, w, rid); r.Status != talkoot.RoutineActive {
		t.Fatalf("an interrupted run must not pause its routine: %+v", r)
	}
}

// 🚨 A run that the last daemon left at work never reported its cost. The
// next daemon ends it as failed, and its routine pauses.
func TestARunThatTheLastDaemonLeftEndsAsFailedAtTheStart(t *testing.T) {
	p := &runScript{reply: "Done."}
	w, seat, clock, cwd, _ := runCrew(t, p, false)
	rid := approved(t, w, seat, ciTask)
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	dir := run.dir
	r, err := talkoot.LoadRoutine(dir, rid)
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	// The root of a run, as a daemon that died in the run left it.
	roster, err := talkoot.Load("crew", talkootEnv())
	if err != nil {
		t.Fatal(err)
	}
	quiet := talkootNativeDriver{sessionOf: func(string, talkoot.Member) (string, error) { return "", errors.New("no session") }}
	rt, err := talkoot.NewRouter(roster, talkoot.OpenRoom(dir), talkoot.Drivers{Native: quiet, Worker: quiet}, talkoot.DefaultLimits(), nil)
	if err != nil {
		t.Fatal(err)
	}
	left, _, err := rt.StartRun(talkoot.RunStart{Routine: r, Session: "20260101-000000-left"})
	if err != nil {
		t.Fatal(err)
	}

	w2 := wakeDaemon(t, cwd, clock, p.serve)
	if got := routineOf(t, w2, rid); got.Status != talkoot.RoutinePaused || !strings.Contains(got.Reason, "the daemon stopped before the run ended") {
		t.Fatalf("the routine of a run that never reported must pause, and say why: %+v", got)
	}
	if turns := runTurns(t); len(turns) != 1 || turns[0].Chain != left.ID || turns[0].Guard != talkoot.GuardFailed {
		t.Fatalf("the room must record the end of the run that was left: %+v", turns)
	}
	if !w2.isTalkootMember("20260101-000000-left") {
		t.Error("the session of a run in the room must count as a member's after a restart")
	}
	// The timer stayed in its file. Its firing reads the record and goes.
	fire(t, w2, clock, 16*time.Minute)
	if n := len(runRoots(t)); n != 1 {
		t.Fatalf("a paused routine started a run after the restart: %d roots", n)
	}
	if ts := routineTimers(w2); len(ts) != 0 {
		t.Fatalf("the timer of the paused routine must go at its firing: %+v", ts)
	}
}

// A watcher runs a command and no session, and the command is not built
// yet. Its firing starts nothing, and the list says so.
func TestAWatcherStartsNoRun(t *testing.T) {
	p := &runScript{reply: "Done."}
	w, seat, clock, _, _ := runCrew(t, p, false)
	rid := approved(t, w, seat, talkoot.RoutineAsk{Kind: talkoot.RoutineWatcher, Schedule: "every 15m", Command: "gh pr checks 12"})
	fire(t, w, clock, 16*time.Minute)
	if n := len(runRoots(t)); n != 0 {
		t.Fatalf("a watcher started %d runs", n)
	}
	r := routineOf(t, w, rid)
	if r.Status != talkoot.RoutineActive || !strings.Contains(r.Problem, "does not run yet") || len(routineTimers(w)) != 1 {
		t.Fatalf("a watcher stays active with its timer, and the list says that it does not run yet: %+v", r)
	}
}

// 🚨 A failed run keeps its routine from the next firing until the routine
// is paused. The router lets a routine run again as soon as its last run
// ended, so an end that was in the room before the pause would let a firing
// in between start a run that the pause does not stop.
func TestAFailedRunEndsAndPausesItsRoutineInOneStep(t *testing.T) {
	p := &runScript{reply: "Done."}
	w, seat, clock, _, _ := runCrew(t, p, true)
	rid := approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	select {
	case <-p.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the run made no request")
	}
	sess := routineOf(t, w, rid).RunSession
	runSeat, ok := w.talkootSeatOf(sess)
	if !ok {
		t.Fatal("the run holds no seat")
	}
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	// A firing holds this lock while it reads the routine and starts a run.
	run.routineMu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.endRunOnce(sess, runSeat.b, talkoot.RunEnd{Failed: "refused"})
	}()
	time.Sleep(150 * time.Millisecond)
	open := routineOf(t, w, rid).RunSession
	run.routineMu.Unlock()
	if open == "" {
		t.Fatal("the failed run's end was in the room before its routine could be paused, so a firing could start the next run")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the run did not end")
	}
	if r := routineOf(t, w, rid); r.Status != talkoot.RoutinePaused || r.RunSession != "" {
		t.Fatalf("the run must end and its routine must pause: %+v", r)
	}
}

// postureUpdate changes helm's posture, to another value at each call.
func postureUpdate(t *testing.T, w *Workspace, cwd string) func() {
	n := 0
	return func() {
		n++
		posture := "ask"
		if n%2 == 0 {
			posture = "auto-edit"
		}
		next := strings.Replace(string(crewText(cwd)), "role: coordinator\n", "role: coordinator\n    posture: "+posture+"\n", 1)
		if _, err := w.talkootUpdate(t.Context(), "crew", "sothr", []byte(next)); err != nil {
			t.Errorf("the roster update: %v", err)
		}
	}
}

// 🚨 A run's session is made from the member as the roster held it a moment
// before the run's seat is set. A roster update in that moment finds no seat
// to stop. The run must see the change itself, and start again from the new
// roster.
func TestARosterUpdateWhileARunStartsMakesTheRunStartAgain(t *testing.T) {
	defer func(fn func()) { runPrepared = fn }(runPrepared)
	p := &runScript{reply: "Done."}
	w, seat, clock, cwd, _ := runCrew(t, p, false)
	rid := approved(t, w, seat, ciTask)
	update := postureUpdate(t, w, cwd)
	tries := 0
	runPrepared = func() {
		if tries++; tries == 1 {
			update()
		}
	}
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "the report", func() bool { return len(runEnds(t, talkoot.KindReport)) == 1 })
	if tries != 2 {
		t.Fatalf("a roster update between the member's read and the run's seat must make the run start again, got %d tries", tries)
	}
	if n := len(runRoots(t)); n != 1 {
		t.Fatalf("the try that the update overtook must leave no root, got %d roots", n)
	}
	if r := routineOf(t, w, rid); r.Runs != 1 {
		t.Fatalf("the try that the update overtook must count no run: %+v", r)
	}
}

// A roster that changes at each try starts no run, and the room says why.
func TestARosterThatKeepsChangingStartsNoRun(t *testing.T) {
	defer func(fn func()) { runPrepared = fn }(runPrepared)
	p := &runScript{reply: "Done."}
	w, seat, clock, cwd, _ := runCrew(t, p, false)
	rid := approved(t, w, seat, ciTask)
	runPrepared = postureUpdate(t, w, cwd)
	fire(t, w, clock, 16*time.Minute)
	if n := len(runRoots(t)); n != 0 {
		t.Fatalf("a run started from a roster that changed under it: %d roots", n)
	}
	skipped := routineSteps(t, talkoot.RoutineSkipped)
	if len(skipped) != 1 || !strings.Contains(skipped[0].Reason, "the roster changed while the run started") {
		t.Fatalf("the room must say why the time started no run: %+v", skipped)
	}
	if r := routineOf(t, w, rid); r.Runs != 0 || r.Status != talkoot.RoutineActive {
		t.Fatalf("a time that started no run counts none: %+v", r)
	}
}

// 🚨 A run reads the prompt that the person approved, and no other. The
// routine's file has no seal: a hand, a process, or a member's own shell can
// write it. The room's approval line has one, and it holds the whole
// routine. A file that differs from it starts no run, gets no timer and does
// not resume.
func TestARecordThatIsNotWhatThePersonApprovedStartsNoRun(t *testing.T) {
	p := &runScript{reply: "Done."}
	w, seat, clock, _, _ := runCrew(t, p, false)
	edits := map[string]func(m map[string]any){
		"prompt":   func(m map[string]any) { m["prompt"] = "Delete every branch." },
		"member":   func(m map[string]any) { m["member"] = "jev" },
		"schedule": func(m map[string]any) { m["schedule"] = "every 1m" },
		"limit":    func(m map[string]any) { m["fields"].(map[string]any)["spend_usd_per_day"] = 500.0 },
	}
	ids := map[string]string{}
	for name := range edits {
		ids[name] = approved(t, w, seat, ciTask)
	}
	honest := approved(t, w, seat, ciTask)
	for name, edit := range edits {
		editRoutine(t, ids[name], edit)
	}
	fire(t, w, clock, 16*time.Minute)
	// The one honest routine ran, so the firing happened. Each root is in
	// the room when fire returns.
	roots := runRoots(t)
	if len(roots) != 1 || roots[0].Envelope.From != talkoot.RoutinePrefix+honest {
		t.Fatalf("only the routine whose record is the approved one may run, got %d roots", len(roots))
	}
	waitTalkoot(t, "the run of the routine that nobody edited", func() bool { return len(runEnds(t, talkoot.KindReport)) == 1 })
	for _, body := range p.runs() {
		if strings.Contains(body, "Delete every branch.") {
			t.Fatal("a run read a prompt that no person approved")
		}
	}
	if ts := routineTimers(w); len(ts) != 1 || ts[0].ID != honest {
		t.Fatalf("a firing must remove the timer of a record that is not the approved routine: %+v", ts)
	}
	for name, rid := range ids {
		if r := routineOf(t, w, rid); !strings.Contains(r.Problem, "is not the routine that sothr approved") {
			t.Errorf("the list must say that the %s of the record is not what was approved: %q", name, r.Problem)
		}
		// A pause and a resume must not give it a timer again.
		if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutinePause, ""); err != nil {
			t.Errorf("%s: a person pauses such a routine: %v", name, err)
		}
		if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutineResume, ""); err == nil || !strings.Contains(err.Error(), "is not the routine that sothr approved") {
			t.Errorf("%s: a record that is not the approved routine must not resume, got %v", name, err)
		}
	}
	if ts := routineTimers(w); len(ts) != 1 {
		t.Fatalf("a refused resume set a timer: %+v", ts)
	}
}

// 🚨 A paused routine whose record a hand set back to active starts no run.
// The room says paused, and the room decides.
func TestARecordSetToActiveUnderAPauseStartsNoRun(t *testing.T) {
	p := &runScript{reply: "Done."}
	w, seat, clock, cwd, _ := runCrew(t, p, false)
	rid := approved(t, w, seat, ciTask)
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	r, err := talkoot.LoadRoutine(run.dir, rid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setRoutine(t, w, rid, ctrlproto.TalkootRoutinePause, "Too noisy."); err != nil {
		t.Fatal(err)
	}
	editRoutine(t, rid, func(m map[string]any) { m["status"] = talkoot.RoutineActive })
	sched, _, err := routineSchedule(r.Schedule, clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.routineTimer(w.Timers(), "crew", r, sched); err != nil {
		t.Fatal(err)
	}
	fire(t, w, clock, 16*time.Minute)
	if n := len(runRoots(t)); n != 0 {
		t.Fatalf("a routine that a person paused started %d runs", n)
	}
	if ts := routineTimers(w); len(ts) != 0 {
		t.Fatalf("the firing must remove the timer: %+v", ts)
	}
	if got := routineOf(t, w, rid); !strings.Contains(got.Problem, "the room says that routine "+rid+" is paused") {
		t.Fatalf("the list must say that the room holds a pause: %+v", got)
	}
	// The next daemon reads the room again, and sets no timer.
	_ = w.Close()
	w2 := wakeDaemon(t, cwd, clock, p.serve)
	if ts := routineTimers(w2); len(ts) != 0 {
		t.Fatalf("a restart set the timer of a routine that the room says is paused: %+v", ts)
	}
}

// 🚨 The room counts a routine's runs, and its count has a seal. A record
// that a hand set back to no runs gets no more runs than its approval gave.
func TestARecordThatCountsFewerRunsGetsNoMoreOfThem(t *testing.T) {
	p := &runScript{reply: "Done."}
	w, seat, clock, _, _ := runCrew(t, p, false)
	ask := ciTask
	ask.EndAfterRuns = new(2)
	rid := approved(t, w, seat, ask)
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "the first report", func() bool { return len(runEnds(t, talkoot.KindReport)) == 1 })
	editRoutine(t, rid, func(m map[string]any) { delete(m, "runs") })
	fire(t, w, clock, 16*time.Minute)
	waitTalkoot(t, "the second report", func() bool { return len(runEnds(t, talkoot.KindReport)) == 2 })
	if r := routineOf(t, w, rid); r.Status != talkoot.RoutineEnded || r.Runs != 2 {
		t.Fatalf("the second run is the last, whatever the record counted: %+v", r)
	}
	if ts := routineTimers(w); len(ts) != 0 {
		t.Fatalf("an ended routine holds no timer: %+v", ts)
	}
}

// 🚨 A daemon that stops in order pauses no routine. A run that has its root
// and no turn yet spent nothing: the stop ends it as interrupted. Left in the
// room, it would read as a run that the daemon died under, and the next
// start would pause its routine.
func TestARunWithNoTurnYetEndsAsInterruptedWhenTheDaemonStops(t *testing.T) {
	defer func(fn func()) { runSeated = fn }(runSeated)
	p := &runScript{reply: "Done."}
	w, seat, clock, cwd, _ := runCrew(t, p, false)
	rid := approved(t, w, seat, ciTask)
	runSeated = w.closeTalkoots
	clock.advance(16 * time.Minute)
	w.Timers().Poll()
	// The talkoot is closed, so no router is there to ask (waitRunEnds). An
	// interrupted run has a turn line and nothing after it.
	waitTalkoot(t, "the end of the run that got no turn", func() bool { return len(runTurns(t)) == 1 })
	// The root is there, so the run did begin.
	if n := len(runRoots(t)); n != 1 {
		t.Fatalf("the run must have its root before the daemon stops, got %d roots", n)
	}
	if tl := runTurns(t)[0]; !tl.Interrupted || tl.Guard != "" || tl.CostUSD != 0 {
		t.Fatalf("a run that got no turn ends as interrupted, with no cost and no failure: %+v", tl)
	}
	_ = w.Close()
	if n := len(p.runs()); n != 0 {
		t.Fatalf("the run made %d requests after the daemon closed its teams", n)
	}
	w2 := wakeDaemon(t, cwd, clock, p.serve)
	if r := routineOf(t, w2, rid); r.Status != talkoot.RoutineActive {
		t.Fatalf("a stop in order must leave the routine active: %+v", r)
	}
	if n := len(runTurns(t)); n != 1 {
		t.Fatalf("the next daemon must find no run at work, got %d ends", n)
	}
	if ts := routineTimers(w2); len(ts) != 1 {
		t.Fatalf("the routine keeps its timer: %+v", ts)
	}
}

// 🚨 A roster update that removes a member stops the member's run. The run's
// session is not the member's own, so the update must find it by its seat.
func TestARosterUpdateThatRemovesTheMemberStopsItsRun(t *testing.T) {
	p := &runScript{reply: "Done."}
	w, _, clock, cwd, _ := runCrew(t, p, true)
	rid := approved(t, w, seatAfterOneTurn(t, w, "jev"), ciTask)
	fire(t, w, clock, 16*time.Minute)
	select {
	case <-p.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the run made no request")
	}
	next := strings.Replace(string(crewText(cwd)), "  - id: jev\n    role: specialist\n", "", 1)
	if next == string(crewText(cwd)) {
		t.Fatal("the test roster has no jev to remove")
	}
	if _, err := w.talkootUpdate(t.Context(), "crew", "sothr", []byte(next)); err != nil {
		t.Fatal(err)
	}
	waitRunEnds(t, w, "the run to end", 1)
	if tl := runTurns(t)[0]; !tl.Interrupted || tl.Member != "jev" {
		t.Fatalf("the run of a member that left is interrupted, and its cost is the member's: %+v", tl)
	}
	if len(runEnds(t, talkoot.KindReport)) != 0 || len(runEnds(t, talkoot.KindNote)) != 0 {
		t.Error("a run whose member left sends nothing")
	}
	fire(t, w, clock, 16*time.Minute)
	if n := len(runRoots(t)); n != 1 {
		t.Fatalf("a routine whose member left starts no more runs, got %d roots", n)
	}
	if r := routineOf(t, w, rid); !strings.Contains(r.Problem, "jev") {
		t.Fatalf("the list must say that the member is gone: %+v", r)
	}
}

// 🚨 A team that closed gave up its lock, and the next daemon may hold it. A
// run that fails after that writes no record and no line. The room still
// shows the run as at work, so the next start ends it and pauses its routine.
func TestARunThatFailsAfterItsTeamClosedWritesNothing(t *testing.T) {
	defer func(d time.Duration) { talkootCloseWait = d }(talkootCloseWait)
	talkootCloseWait = 50 * time.Millisecond
	p := &runScript{reply: "Done."}
	w, seat, clock, cwd, _ := runCrew(t, p, true)
	rid := approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	select {
	case <-p.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the run made no request")
	}
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	var open []talkoot.Run
	_ = run.do(func(rt *talkoot.Router) error { open = rt.OpenRuns(); return nil })
	if len(open) != 1 {
		t.Fatalf("want the one run at work, got %d", len(open))
	}
	w.closeTalkoots()
	before := len(roomLines(t, "crew", func(talkoot.Line) bool { return true }))
	w.finishRun(run, open[0], talkoot.RunEnd{Failed: "the provider refused the request"}, false)
	if r, err := talkoot.LoadRoutine(run.dir, rid); err != nil || r.Status != talkoot.RoutineActive {
		t.Fatalf("a run that failed after its team closed must not write the routine's record: %+v, %v", r, err)
	}
	if after := len(roomLines(t, "crew", func(talkoot.Line) bool { return true })); after != before {
		t.Fatalf("a run that failed after its team closed wrote %d lines to a room it no longer holds", after-before)
	}
	// The next daemon finds the run at work, ends it, and pauses the routine.
	_ = w.Close()
	w2 := wakeDaemon(t, cwd, clock, p.serve)
	if r := routineOf(t, w2, rid); r.Status != talkoot.RoutinePaused || !strings.Contains(r.Reason, "the daemon stopped before the run ended") {
		t.Fatalf("the next start must end the run that the room shows at work, and pause its routine: %+v", r)
	}
}

// 🚨 A roster update between a run's seat and its turn finds no turn to
// stop. The turn must not start then: it would work with the posture and the
// directory that the update took away. The run ends as interrupted, and its
// routine stays active.
func TestARosterUpdateBeforeTheRunsTurnStartsNoTurn(t *testing.T) {
	defer func(fn func()) { runSeated = fn }(runSeated)
	p := &runScript{reply: "Done."}
	w, seat, clock, cwd, _ := runCrew(t, p, false)
	rid := approved(t, w, seat, ciTask)
	runSeated = postureUpdate(t, w, cwd)
	fire(t, w, clock, 16*time.Minute)
	waitRunEnds(t, w, "the end of the run", 1)
	// The root is there, so the run did begin.
	if n := len(runRoots(t)); n != 1 {
		t.Fatalf("the run must have its root before the update, got %d roots", n)
	}
	if tl := runTurns(t)[0]; !tl.Interrupted || tl.Guard != "" || tl.CostUSD != 0 {
		t.Fatalf("a run that the update overtook ends as interrupted, with no cost: %+v", tl)
	}
	if n := len(p.runs()); n != 0 {
		t.Fatalf("the run made %d requests as a member that the update changed", n)
	}
	if r := routineOf(t, w, rid); r.Status != talkoot.RoutineActive {
		t.Fatalf("a run that an update stopped must not pause its routine: %+v", r)
	}
}

// 🚨 A run is one turn. A turn that its session opens after the run ended
// has no run to count its cost, so it does not run.
func TestATurnInTheSessionOfARunThatEndedDoesNotRun(t *testing.T) {
	p := &runScript{reply: "Done."}
	w, seat, clock, _, _ := runCrew(t, p, true)
	rid := approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	select {
	case <-p.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the run made no request")
	}
	sid := routineOf(t, w, rid).RunSession
	w.talkoot.mu.Lock()
	b := w.talkoot.seats[sid]
	w.talkoot.mu.Unlock()
	if b == nil || b.routine == nil {
		t.Fatalf("the run's session %q holds no seat of a run", sid)
	}
	// While the run works, its session may open a turn: the positive control.
	end, _, closing := w.talkootTurn(sid)
	if end == nil || closing {
		t.Fatal("the session of a run at work must get the run's callbacks")
	}
	w.talkoot.turns.Add(-1)
	// The run is over, and its session has not closed yet.
	b.routine.ended.Store(true)
	if end, publish, closing := w.talkootTurn(sid); end != nil || publish != nil || !closing {
		t.Fatal("the session of a run that ended must be refused its turn")
	}
}
