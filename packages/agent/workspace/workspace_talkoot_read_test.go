package workspace

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// heldProvider answers every request with a short reply, or with a 400 when
// fail is set. The first request waits for release, so the member it serves
// stays busy.
func heldProvider(release <-chan struct{}, entered chan<- struct{}, fail bool) http.HandlerFunc {
	var n atomic.Int64
	return func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			entered <- struct{}{}
			select {
			case <-r.Context().Done():
				return
			case <-release:
			}
		}
		if fail {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"refused"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
}

// openHeldWorkspace opens a workspace over heldProvider and makes the crew
// talkoot. release frees the held request. It runs at cleanup too, before the
// workspace closes, because nothing else cancels that request and the
// server's Close waits on it.
func openHeldWorkspace(t *testing.T) (w *Workspace, release func(), entered <-chan struct{}) {
	t.Helper()
	return openHeldWorkspaceWith(t, func(release <-chan struct{}, entered chan<- struct{}) http.HandlerFunc {
		return heldProvider(release, entered, false)
	})
}

// openHeldWorkspaceWith is openHeldWorkspace over the handler that mk builds.
func openHeldWorkspaceWith(t *testing.T, mk func(release <-chan struct{}, entered chan<- struct{}) http.HandlerFunc) (w *Workspace, release func(), entered <-chan struct{}) {
	t.Helper()
	cwd := talkootHome(t)
	ch, in := make(chan struct{}), make(chan struct{}, 1)
	w = openTalkootWorkspaceWith(t, cwd, mk(ch, in))
	var once sync.Once
	release = func() { once.Do(func() { close(ch) }) }
	t.Cleanup(release)
	if _, err := w.talkootCreate(context.Background(), "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	return w, release, in
}

func readLine(t *testing.T, member, ref string) (talkoot.Line, bool) {
	t.Helper()
	ls := roomLines(t, "crew", func(l talkoot.Line) bool {
		return l.Type == talkoot.LineRead && l.Member == member && l.Ref == ref
	})
	if len(ls) == 0 {
		return talkoot.Line{}, false
	}
	return ls[0], true
}

// busyHelm starts a talkoot, wakes helm with a first post, and holds helm's
// turn in its model call. It then posts again, and returns both posts.
func busyHelm(t *testing.T, w *Workspace, entered <-chan struct{}) (first, second talkoot.Envelope) {
	t.Helper()
	ctx := context.Background()
	first, err := w.talkootPost(ctx, "crew", "sothr", nil, "Plan it.", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	second, err = w.talkootPost(ctx, "crew", "sothr", nil, "Also check the index.", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return first, second
}

func sendAs(t *testing.T, w *Workspace, member string) talkoot.Envelope {
	t.Helper()
	seat, ok := w.talkootSeatOf(memberView(t, w, "crew", member).Session)
	if !ok {
		t.Fatalf("%s holds no seat", member)
	}
	e, err := seat.Send(talkoot.Outgoing{To: []string{"jev"}, Kind: talkoot.KindNote, Body: fmt.Sprintf("note %d", sendSeq.Add(1))})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

var sendSeq atomic.Int64

// 🔑 A busy member keeps its chain while the second post waits in its queue,
// and takes the post's chain when its turn reads the text. The room records
// that moment. TestABusyMemberKeepsItsChainUntilItReads replays it.
func TestABusyMemberTakesTheChainWhenItsTurnReadsTheEnvelope(t *testing.T) {
	w, release, entered := openHeldWorkspace(t)
	first, second := busyHelm(t, w, entered)

	if l, ok := readLine(t, "helm", first.ID); !ok || l.Chain != first.ID {
		t.Errorf("the turn that began with the first post read it: %+v, %v", l, ok)
	}
	if _, ok := readLine(t, "helm", second.ID); ok {
		t.Error("the second post is read while helm's turn is still in its model call")
	}
	if e := sendAs(t, w, "helm"); e.Chain.Root != first.ID {
		t.Errorf("while the second post waits, helm sends in %q, want %q", e.Chain.Root, first.ID)
	}

	release()
	waitTalkoot(t, "helm to read the second post", func() bool {
		_, ok := readLine(t, "helm", second.ID)
		return ok
	})
	if e := sendAs(t, w, "helm"); e.Chain.Root != second.ID {
		t.Errorf("after the read, helm sends in %q, want %q", e.Chain.Root, second.ID)
	}
	// The turn drains the queue at its closing boundary and goes on, so it
	// reads the second post before it ends, and its line names that chain.
	waitTalkoot(t, "helm's turn to end", func() bool {
		return len(roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineTurn && l.Member == "helm" })) > 0
	})
	var order []string
	for _, l := range roomLines(t, "crew", func(l talkoot.Line) bool {
		return l.Member == "helm" && (l.Type == talkoot.LineRead || l.Type == talkoot.LineTurn)
	}) {
		switch {
		case l.Type == talkoot.LineTurn && l.Chain == second.ID:
			order = append(order, "turn in second")
		case l.Type == talkoot.LineTurn:
			order = append(order, "turn in "+l.Chain)
		case l.Ref == first.ID:
			order = append(order, "read first")
		case l.Ref == second.ID:
			order = append(order, "read second")
		}
	}
	if len(order) < 3 || order[0] != "read first" || order[1] != "read second" || order[2] != "turn in second" {
		t.Errorf("the room reads %v, want the first read, the second read, then a turn in the second chain", order)
	}
}

// 🚨 A turn that fails drops what a person queued behind it, but each talkoot
// envelope stays queued, in order. It starts no turn. The member keeps its chain until
// its next turn reads the envelope (TKT-01M3B4KK).
func TestAFailedTurnKeepsTheEnvelopeForTheNextTurn(t *testing.T) {
	w, release, entered := openHeldWorkspaceWith(t, failFirst)
	first, second := busyHelm(t, w, entered)
	s, err := w.resolve(memberView(t, w, "crew", "helm").Session)
	if err != nil {
		t.Fatal(err)
	}
	s.queue("A stale follow-up.")
	third, err := w.talkootPost(context.Background(), "crew", "sothr", nil, "And the tests.", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	release()
	waitTalkoot(t, "helm's turn to end", func() bool {
		return len(roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineTurn && l.Member == "helm" })) > 0
	})
	queued := s.agent.PendingQueuedMessages()
	if len(queued) != 2 || !strings.Contains(queued[0], second.ID) || !strings.Contains(queued[1], third.ID) {
		t.Fatalf("after the failed turn the queue holds %q, want the second and third posts, in order", queued)
	}
	if _, ok := readLine(t, "helm", second.ID); ok {
		t.Error("the kept envelope was read before a turn read it")
	}
	resumeFailed(t, w, "crew", "helm")
	if e := sendAs(t, w, "helm"); e.Chain.Root != first.ID {
		t.Errorf("helm sends in %q, want the chain it read, %q", e.Chain.Root, first.ID)
	}

	// A new post starts helm's next turn. The kept posts are older, so the
	// turn reads them first, and helm ends in the new post's chain.
	fourth, err := w.talkootPost(context.Background(), "crew", "sothr", nil, "Then report.", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm's next turn to read every post", func() bool {
		_, ok := readLine(t, "helm", fourth.ID)
		return ok
	})
	var order []string
	for _, l := range roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineRead && l.Member == "helm" }) {
		order = append(order, l.Ref)
	}
	if want := []string{first.ID, second.ID, third.ID, fourth.ID}; !slices.Equal(order, want) {
		t.Errorf("helm read %v, want %v", order, want)
	}
	if e := sendAs(t, w, "helm"); e.Chain.Root != fourth.ID {
		t.Errorf("after the reads, helm sends in %q, want the last read, %q", e.Chain.Root, fourth.ID)
	}
}

// failFirst holds the first request, then refuses it. Every later request
// gets a short reply.
func failFirst(release <-chan struct{}, entered chan<- struct{}) http.HandlerFunc {
	var n atomic.Int64
	held := heldProvider(release, entered, true)
	open := make(chan struct{})
	close(open)
	ok := heldProvider(open, make(chan struct{}, 1), false)
	return func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			held(w, r)
			return
		}
		ok(w, r)
	}
}

// A read needs the delivered text, in a user message. A person's own message
// reads nothing, and two deliveries with one text are read in order.
func TestAReadNeedsTheDeliveredText(t *testing.T) {
	var r talkootReads
	var mu sync.Mutex
	var got []string
	add := func(text, name string) {
		r.pending = append(r.pending, talkootRead{text: text, read: func() {
			mu.Lock()
			got = append(got, name)
			mu.Unlock()
		}})
	}
	add("[talkoot crew] hi", "a")
	add("[talkoot crew] hi", "b")
	user := func(text string, meta map[string]string) provider.Message {
		return provider.Message{Role: provider.RoleUser, Meta: meta, Content: []provider.Content{provider.TextBlock{Text: text}}}
	}
	r.observe(user("something the person typed", nil))
	r.observe(provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "[talkoot crew] hi"}}})
	r.observe(user("[talkoot crew] hi", map[string]string{core.MetaSynthetic: "true"}))
	if len(got) != 0 {
		t.Fatalf("a message that is not the delivery read %v", got)
	}
	r.observe(user("  [talkoot crew] hi\n", nil))
	if len(got) != 1 || got[0] != "a" {
		t.Errorf("the first read took %v, want the older delivery", got)
	}
	r.forget([]string{"[talkoot crew] hi"})
	r.observe(user("[talkoot crew] hi", nil))
	if len(got) != 1 {
		t.Errorf("a forgotten delivery was read: %v", got)
	}
}

// 🚨 A session that loses its seat before its turn reads the envelope reads it
// as a stranger. The member's chain does not move.
func TestAnUnseatedSessionReadsNothing(t *testing.T) {
	w, release, entered := openHeldWorkspace(t)
	_, second := busyHelm(t, w, entered)
	sessID := memberView(t, w, "crew", "helm").Session
	s, err := w.resolve(sessID)
	if err != nil {
		t.Fatal(err)
	}
	w.unseatTalkoot(sessID)
	release()
	waitTalkoot(t, "the session's turn to take the queued text", func() bool {
		s.reads.mu.Lock()
		defer s.reads.mu.Unlock()
		return len(s.reads.pending) == 0
	})
	if _, ok := readLine(t, "helm", second.ID); ok {
		t.Error("a session with no seat moved helm into the second chain")
	}
}

// 🚨 Every delivery that waits behind a running turn is read, however many
// wait. A bound that dropped the oldest would leave a queued text with no
// receipt.
func TestEveryQueuedDeliveryIsRead(t *testing.T) {
	tool := gatedStepTool{entered: make(chan struct{}, 1), release: make(chan struct{})}
	s := newTurnTestSession(t, &twoStepClient{})
	s.agent.SetTools(core.Registry{"step": tool})
	if err := s.prompt("run the tool", nil, core.UserMessageExtras{}); err != nil {
		t.Fatal(err)
	}
	<-tool.entered
	const n = 300
	var read atomic.Int64
	for i := range n {
		s.queueTalkoot(fmt.Sprintf("[talkoot crew] message %d", i), func() { read.Add(1) })
	}
	close(tool.release)
	waitTalkoot(t, "every queued delivery to be read", func() bool { return read.Load() == n })
}

// A delivery that a user_message guard refuses never reaches a turn, so
// nothing waits to read it.
func TestAGuardRefusalForgetsTheDelivery(t *testing.T) {
	var r talkootReads
	r.pending = []talkootRead{{text: "[talkoot crew] refused", read: func() {}}, {text: "[talkoot crew] kept", read: func() {}}}
	r.onEvent(core.EvUserMessageRejected{Text: "[talkoot crew] refused", Reason: "blocked"})
	if len(r.pending) != 1 || r.pending[0].text != "[talkoot crew] kept" {
		t.Errorf("after the refusal, waiting: %+v", r.pending)
	}
}

// 🚨 A native driver that cannot report a read must not deliver. The router
// would leave the member's chain alone, and nothing would move it.
func TestANativeDriverWithoutAReadReportRefuses(t *testing.T) {
	s := newTurnTestSession(t, &promptRecorder{})
	d := talkootNativeDriver{sessionOf: bindOnly("helm", s.id), resolve: sessionResolver(s)}
	var r talkoot.Receipt
	if err := d.DeliverRead("crew", talkoot.Member{ID: "helm"}, "x", r); err == nil {
		t.Error("a native driver with no read report delivered")
	}
}

// A turn a person interrupts was asked to stop, so it does not pause the
// member the way a failed turn does. Its turn line says it was interrupted,
// and the delivery that started it says it woke helm.
func TestAnInterruptedTurnDoesNotPauseTheMember(t *testing.T) {
	w, _, entered := openHeldWorkspaceWith(t, func(release <-chan struct{}, entered chan<- struct{}) http.HandlerFunc {
		return heldProvider(release, entered, true)
	})
	if _, err := w.talkootPost(context.Background(), "crew", "sothr", nil, "Plan it.", nil, ""); err != nil {
		t.Fatal(err)
	}
	<-entered
	s, err := w.resolve(memberView(t, w, "crew", "helm").Session)
	if err != nil {
		t.Fatal(err)
	}
	s.interruptTurn()
	waitTalkoot(t, "helm's turn to end", func() bool {
		return len(roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineTurn && l.Member == "helm" })) > 0
	})
	if st := memberView(t, w, "crew", "helm").Status; st.Paused != "" || len(st.Pauses) != 0 {
		t.Errorf("an interrupted turn paused helm: %q %v", st.Paused, st.Pauses)
	}
	turns := roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineTurn && l.Member == "helm" })
	if len(turns) != 1 || !turns[0].Interrupted {
		t.Errorf("helm's turn lines = %+v, want one that says it was interrupted", turns)
	}
	ds := roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineDelivery && l.Member == "helm" })
	if len(ds) != 1 || !ds[0].Woke {
		t.Errorf("helm's delivery lines = %+v, want one that woke it", ds)
	}
}

// An interrupt that arrives after a turn returned cleanly finds a finished
// turn, so its turn line does not say interrupted.
func TestALateInterruptDoesNotMarkAFinishedTurn(t *testing.T) {
	w, _, entered := openHeldWorkspaceWith(t, func(release <-chan struct{}, entered chan<- struct{}) http.HandlerFunc {
		return heldProvider(release, entered, true)
	})
	if _, err := w.talkootPost(context.Background(), "crew", "sothr", nil, "Plan it.", nil, ""); err != nil {
		t.Fatal(err)
	}
	<-entered
	s, err := w.resolve(memberView(t, w, "crew", "helm").Session)
	if err != nil {
		t.Fatal(err)
	}
	helmTurns := func() []talkoot.Line {
		return roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineTurn && l.Member == "helm" })
	}
	s.interruptTurn()
	waitTalkoot(t, "the first turn to end", func() bool { return len(helmTurns()) == 1 })
	var turnCtx context.Context
	waitTalkoot(t, "the turn slot", func() bool {
		ctx, err := s.beginTurn()
		turnCtx = ctx
		return err == nil
	})
	s.launchTurn(turnCtx, func(context.Context) error { return nil }, s.interruptTurn)
	waitTalkoot(t, "the second turn to end", func() bool { return len(helmTurns()) == 2 })
	if l := helmTurns()[1]; l.Interrupted {
		t.Errorf("a turn that finished before the interrupt says interrupted: %+v", l)
	}
}

// A turn a person starts in a member's session is a turn the router sees: the
// member shows as working, and a delivery joins the turn rather than waking
// the member. A delivery to the idle member still wakes it (TKT-01M3SMDS89).
func TestATurnAPersonStartsShowsTheMemberWorking(t *testing.T) {
	w, release, entered := openHeldWorkspace(t)
	if _, err := w.talkootPost(context.Background(), "crew", "sothr", []string{"helm"}, "Plan it.", nil, ""); err != nil {
		t.Fatal(err)
	}
	<-entered
	release()
	working := func() bool { return memberView(t, w, "crew", "helm").Status.Working }
	waitTalkoot(t, "helm's first turn to end", func() bool { return !working() })
	deliveries := func() []talkoot.Line {
		return roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineDelivery && l.Member == "helm" })
	}
	// The delivery claims its member before the session's turn starts, so
	// the count that turn raises cannot hide the wake.
	if ds := deliveries(); len(ds) != 1 || !ds[0].Woke {
		t.Fatalf("the delivery that woke idle helm must say woke: %+v", ds)
	}
	s, err := w.resolve(memberView(t, w, "crew", "helm").Session)
	if err != nil {
		t.Fatal(err)
	}

	var turnCtx context.Context
	waitTalkoot(t, "the turn slot", func() bool {
		ctx, err := s.beginTurn()
		turnCtx = ctx
		return err == nil
	})
	hold := make(chan struct{})
	s.launchTurn(turnCtx, func(context.Context) error { <-hold; return nil }, nil)
	waitTalkoot(t, "helm to show as working in the person's turn", working)

	if _, err := w.talkootPost(context.Background(), "crew", "sothr", []string{"helm"}, "And this.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the second delivery", func() bool { return len(deliveries()) == 2 })
	if l := deliveries()[1]; l.Woke {
		t.Errorf("a delivery into the person's turn says it woke helm: %+v", l)
	}
	close(hold)
	waitTalkoot(t, "helm to finish its turns", func() bool { return !working() })
}
