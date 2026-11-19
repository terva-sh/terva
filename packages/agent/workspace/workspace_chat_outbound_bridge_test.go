package workspace

// The outbound policy behind a real bridge: a talkoot over a stub provider, a
// fake connector, and the pump between them.

import (
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
)

// awayFromChat makes every message one that the person is not waiting for,
// and keeps the team's quiet moment from sending anything until the test
// says so.
func awayFromChat(t *testing.T) {
	t.Helper()
	presence, settle := chatPresence, chatTeamSettleWait
	chatPresence, chatTeamSettleWait = 0, time.Hour
	t.Cleanup(func() { chatPresence, chatTeamSettleWait = presence, settle })
}

// teamPump returns the pump of crew's bridge.
func teamPump(t *testing.T, w *Workspace) *chatTeamPump {
	t.Helper()
	w.chat.mu.Lock()
	defer w.chat.mu.Unlock()
	for _, b := range w.chat.bridges {
		if b.talkoot == "crew" && b.pump != nil {
			return b.pump
		}
	}
	t.Fatal("crew's bridge runs no pump")
	return nil
}

// heldInView returns what the team's view says the bridge holds.
func heldInView(t *testing.T, w *Workspace) *ctrlproto.ChatOutbound {
	t.Helper()
	v, err := w.Talkoot(t.Context(), ctrlproto.TalkootRef{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Chat == nil || v.Chat.Outbound == nil {
		t.Fatalf("the team's view has no outbound state: %+v", v.Chat)
	}
	return v.Chat.Outbound
}

const greenBuild = `{"to":["human"],"kind":"message","body":"The build is green."}`

// With the person away, a member's message waits. The app shows it. When the
// team stays quiet, the message goes out as the last word on the work.
func TestAMessageWaitsUntilTheTeamIsQuiet(t *testing.T) {
	awayFromChat(t)
	w, conn := startTeamChat(t, toolCallProvider("talkoot_send", greenBuild), "plan", 0)

	conn.dm("how is the build?")
	waitFor(t, "the message to be held", func() bool {
		return len(heldInView(t, w).Held) == 1
	})
	out := heldInView(t, w)
	if h := out.Held[0]; h.Text != "The build is green." || h.Reason != "" || h.At.IsZero() {
		t.Fatalf("held = %+v, want the message as an update", h)
	}
	if !out.BriefOnReply || out.Quiet {
		t.Fatalf("outbound = %+v, want the brief on the person's next message and no quiet hours", out)
	}
	if got := conn.outbound(); slices.ContainsFunc(got, has("The build is green.")) {
		t.Fatalf("the message went out while the team still worked: %q", got)
	}

	// The team stops, and the wait after that passes.
	p := teamPump(t, w)
	waitFor(t, "the pump to see the team stop", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return !p.settleAt.IsZero()
	})
	p.mu.Lock()
	p.settleAt = time.Now()
	p.mu.Unlock()
	p.timerFired(false)
	conn.waitOutbound(t, "the last word", func(s string) bool { return s == "The build is green." })
	if n := len(heldInView(t, w).Held); n != 0 {
		t.Fatalf("%d messages are still held after the last word went out", n)
	}
}

// A member that asks a question has not finished. The team is off work then,
// because the turn waits, and that must not send the held update as the last
// word. The work goes on when the person answers.
func TestAQuestionIsNotTheEndOfTheWork(t *testing.T) {
	awayFromChat(t)
	w, conn := startTeamChat(t, askProvider(), "auto-edit", 0)

	conn.dm("which port do we use?")
	conn.waitOutbound(t, "the question", has("Which port?"))
	waitTalkoot(t, "the team to wait on the person", func() bool {
		v, err := w.Talkoot(t.Context(), ctrlproto.TalkootRef{ID: "crew"})
		return err == nil && v.State == "needs-you"
	})
	w.chatOutboundFor("crew").memberSaid(time.Now(), "e-held", "c1", "", "looking at the config")

	// The wait after the team stopped is over.
	p := teamPump(t, w)
	p.mu.Lock()
	p.settleAt = time.Now()
	p.mu.Unlock()
	p.timerFired(false)
	waitFor(t, "the pump to take up the question whether the work finished", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.settleAt.IsZero()
	})
	// The pump clears its settle time before it decides. It handles one
	// thing at a time, so a notice that goes out after this point went out
	// after the decision was made and acted on.
	ev := ctrlproto.TalkootInboxEvent("crew", ctrlproto.TalkootCard{Kind: ctrlproto.TalkootCardKickoff, ID: "kickoff"})
	w.talkootEmit(talkootEvent{Talkoot: "crew", Kind: "inbox", Wire: &ev})
	conn.waitOutbound(t, "the marker after the decision", has("waits for its kickoff"))
	if got := conn.outbound(); slices.ContainsFunc(got, has("looking at the config")) {
		t.Fatalf("an update went out as the last word while a question was open: %q", got)
	}
	if n := len(heldInView(t, w).Held); n != 1 {
		t.Fatalf("%d messages are held, want the update still there", n)
	}
}

// A status sets the settle time at once, and an envelope waits in the pump's
// queue. When both are ready, the envelope is handled first. A settle that
// ran first would send an older update as the last word, and nothing would
// release the real one.
func TestTheLastMessageIsHandledBeforeTheWorkIsCalledFinished(t *testing.T) {
	awayFromChat(t)
	w, conn := startTeamChat(t, quietProvider(), "auto-edit", 0)
	w.chatOutboundFor("crew").memberSaid(time.Now(), "e-old", "c1", "", "tests are running")

	p := teamPump(t, w)
	last := talkoot.Envelope{ID: "e-new", From: "helm", To: []string{talkoot.HumanPrefix + "sothr"}, Kind: talkoot.KindMessage, Body: "tests passed", Chain: talkoot.Chain{Root: "c1"}}
	// The pump waits with its settle time set, a moment ahead.
	p.mu.Lock()
	p.settleAt = time.Now().Add(100 * time.Millisecond)
	p.mu.Unlock()
	p.timerFired(false)
	waitFor(t, "the pump to wait on its settle time", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return !p.quietEnd
	})
	// The envelope joins the queue, and the settle time comes before the
	// pump has looked at it. Nothing wakes the pump but that time.
	p.mu.Lock()
	p.queue = append(p.queue, talkootEvent{Talkoot: "crew", Kind: "envelope", Line: &talkoot.Line{Envelope: &last}})
	p.mu.Unlock()

	got := conn.waitOutbound(t, "the last word", has("tests"))
	if !slices.Contains(got, "tests passed") || slices.ContainsFunc(got, has("tests are running")) {
		t.Fatalf("the chat got %q, want the newest message and not the older update", got)
	}
}

// The brief goes out when the daemon's timer says so, as one message.
func TestTheBriefGoesOutAtItsTime(t *testing.T) {
	awayFromChat(t)
	w, conn := startTeamChat(t, toolCallProvider("talkoot_send", greenBuild), "plan", 0)
	// 07:00 on the daemon's clock, half an hour before the brief.
	clock := &stepClock{now: time.Date(2026, 11, 17, 7, 0, 0, 0, time.Local)}
	w.timers.clock = clock
	w.LoadTimers()
	rules, err := parseChatOutboundRules([]string{"07:30"}, "")
	if err != nil {
		t.Fatal(err)
	}
	w.chatOutboundSet("crew", rules)

	conn.dm("how is the build?")
	waitFor(t, "the message to be held", func() bool {
		return len(heldInView(t, w).Held) == 1
	})
	out := heldInView(t, w)
	if want := time.Date(2026, 11, 17, 7, 30, 0, 0, time.Local); !out.NextBrief.Equal(want) || out.BriefOnReply {
		t.Fatalf("outbound = %+v, want the next brief at %s", out, want)
	}
	// ⚠️ The member's turn can still run here. A post that lands inside it
	// gets no answer of its own, and a post that lands after it gets one, so
	// the brief would list one update on a slow run and two on a fast one.
	// Wait for the turn to end, and then for the answer to the second post.
	waitFor(t, "the member's first turn to end", func() bool {
		v, err := w.Talkoot(t.Context(), ctrlproto.TalkootRef{ID: "crew"})
		return err == nil && v.State != talkoot.TeamBusy
	})
	// The person writes again. With a brief time set, that does not empty
	// the queue.
	conn.dm("thanks")
	waitFor(t, "the answer to the second post to be held", func() bool {
		return len(heldInView(t, w).Held) == 2
	}, func() string {
		var b strings.Builder
		fmt.Fprintf(&b, "held=%+v out=%q\n", heldInView(t, w).Held, conn.outbound())
		for _, e := range roomPosts(t, w) {
			fmt.Fprintf(&b, "room: from=%s to=%v kind=%s body=%q\n", e.From, e.To, e.Kind, e.Body)
		}
		return b.String()
	})
	if got := conn.outbound(); slices.ContainsFunc(got, has("The build is green.")) {
		t.Fatalf("the person's second message emptied the queue before the brief time: %q", got)
	}

	clock.advance(31 * time.Minute)
	w.Timers().Poll()
	got := conn.waitOutbound(t, "the brief", has("2 updates from the team while you were away"))
	brief := got[len(got)-1]
	if !strings.Contains(brief, "the coordinator: The build is green.") || !strings.Contains(brief, "the coordinator: ok") {
		t.Fatalf("the brief = %q, want it to list both held messages", brief)
	}
	waitFor(t, "the queue to empty", func() bool { return len(heldInView(t, w).Held) == 0 })

	// The timers are the daemon's, and a person can see them.
	list, err := w.TimerList(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Timers) != 1 || list.Timers[0].Owner != chatTimerOwner || list.Timers[0].Schedule != "daily 07:30" || list.Timers[0].Problem != "" {
		t.Fatalf("timers = %+v, want the one brief timer, in order", list.Timers)
	}
}

// 🔑 A daemon sets its timers at every start. Set starts a schedule again
// from now, so a second Set would lose the window that passed while the
// daemon was down, and the morning's brief with it.
func TestTheSameRulesDoNotStartTheBriefTimerAgain(t *testing.T) {
	cwd := timersHome(t)
	clock := &stepClock{now: time.Date(2026, 11, 17, 7, 0, 0, 0, time.Local)}
	w := openTimerDaemon(t, cwd, clock)
	rules, err := parseChatOutboundRules([]string{"07:30"}, "22:00-06:00")
	if err != nil {
		t.Fatal(err)
	}
	w.chatOutboundSet("crew", rules)
	end, ok := w.Timers().Get(chatTimerOwner, chatQuietEndTimer("crew"))
	if !ok || end.Schedule.String() != "daily 06:00" {
		t.Fatalf("the end of quiet hours = %+v, %v", end, ok)
	}
	_ = w.Close()

	// The brief's window passes with the daemon down. The next daemon sets
	// the same rules, and the window that passed must still fire. No chat is
	// connected, so the firing is kept as a brief that is due.
	clock.advance(time.Hour)
	w = openTimerDaemon(t, cwd, clock)
	w.chatOutboundSet("crew", rules)
	waitFor(t, "the brief that came due while the daemon was down", func() bool {
		out := w.chatOutboundFor("crew")
		out.mu.Lock()
		defer out.mu.Unlock()
		return out.briefDue
	})

	// Other rules do replace it, and rules with no quiet hours remove that
	// timer.
	other, err := parseChatOutboundRules([]string{"09:00"}, "")
	if err != nil {
		t.Fatal(err)
	}
	w.chatOutboundSet("crew", other)
	moved, _ := w.Timers().Get(chatTimerOwner, chatBriefTimer("crew"))
	if moved.Schedule.String() != "daily 09:00" {
		t.Fatalf("the brief timer is %q after the rules changed", moved.Schedule)
	}
	if _, ok := w.Timers().Get(chatTimerOwner, chatQuietEndTimer("crew")); ok {
		t.Fatal("the end-of-quiet timer stayed after quiet hours were removed")
	}
}

// Quiet hours hold a decision and send it when they end. A question still
// goes out, because a member's turn waits on it.
func TestQuietHoursHoldADecisionAndPassAQuestion(t *testing.T) {
	awayFromChat(t)
	night := time.Date(2026, 11, 17, 23, 0, 0, 0, time.Local)
	// ⚠️ The pump reads the clock on its own goroutine while the test moves
	// it to the morning, so the time is an atomic value. A plain variable
	// here failed the race detector in CI.
	var now atomic.Int64
	now.Store(night.UnixNano())
	chatNow = func() time.Time { return time.Unix(0, now.Load()) }
	t.Cleanup(func() { chatNow = time.Now })

	w, conn := startTeamChat(t, askProvider(), "auto-edit", 0)
	rules, err := parseChatOutboundRules(nil, "22:00-07:00")
	if err != nil {
		t.Fatal(err)
	}
	w.chatOutboundSet("crew", rules)

	// A member asks a question in quiet hours, and it goes out.
	conn.dm("which port do we use?")
	conn.waitOutbound(t, "the question", has("Which port?"))

	// A decision in the same quiet hours waits.
	ev := ctrlproto.TalkootInboxEvent("crew", ctrlproto.TalkootCard{Kind: ctrlproto.TalkootCardKickoff, ID: "kickoff"})
	w.talkootEmit(talkootEvent{Talkoot: "crew", Kind: "inbox", Wire: &ev})
	waitFor(t, "the kickoff notice to be held", func() bool {
		return len(heldInView(t, w).Held) == 1
	})
	out := heldInView(t, w)
	if !out.Quiet || out.Held[0].Reason != outDecision {
		t.Fatalf("outbound = %+v, want quiet hours on and the notice held as a decision", out)
	}
	if got := conn.outbound(); slices.ContainsFunc(got, has("waits for its kickoff")) {
		t.Fatalf("the decision went out in quiet hours: %q", got)
	}

	// Morning. Any event wakes the pump, and the held decision goes out.
	now.Store(night.Add(8 * time.Hour).UnixNano())
	teamPump(t, w).timerFired(false)
	conn.waitOutbound(t, "the held decision", has("waits for its kickoff"))
	if n := len(heldInView(t, w).Held); n != 0 {
		t.Fatalf("%d messages are still held after quiet hours", n)
	}
}

// 🚨 A raw tool card in a chat thread is the noise rule 22 exists to stop,
// and it would carry a command to a service outside the daemon. The chat
// gets the member's name and where to decide, and nothing of the call.
func TestAToolCallNeverReachesTheChat(t *testing.T) {
	w, conn := startTeamChat(t, toolCallProvider("bash", `{"command":"echo secret-marker-7731"}`), "ask", 0)

	conn.dm("run the check")
	conn.waitOutbound(t, "the approval to be named", has("helm waits for an approval"))
	waitTalkoot(t, "the permission card", func() bool { return len(inbox(t, w)) == 1 })
	for _, line := range conn.outbound() {
		if strings.Contains(line, "secret-marker-7731") || strings.Contains(line, "bash") || strings.Contains(line, "echo") {
			t.Fatalf("the chat got a part of the tool call: %q", line)
		}
	}
}
