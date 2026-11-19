package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/timers"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// wakesConfig writes the user configuration of a daemon whose owner turned
// wakes on, with these caps. An empty wakes leaves the key out.
func wakesConfig(t *testing.T, wakes string) {
	t.Helper()
	cfg := `{"talkoot_enabled": true}`
	if wakes != "" {
		cfg = `{"talkoot_enabled": true, "talkoot_wakes": ` + wakes + `}`
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("TERVA_HOME"), "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

// wakeDaemon is a daemon in cwd as terva web starts one: its talkoots, then
// its timers, over a clock that only the test moves.
func wakeDaemon(t *testing.T, cwd string, clock *stepClock, prov http.HandlerFunc) *Workspace {
	t.Helper()
	w := openTalkootWorkspaceWith(t, cwd, prov)
	w.timers.clock = clock
	w.LoadTalkoots()
	w.LoadTimers()
	if w.Timers() == nil {
		t.Fatalf("the daemon runs no timer service: %q", w.timers.problem)
	}
	return w
}

// wakeCrew is the crew talkoot on a daemon with wakes on, after one turn of
// helm's. So helm holds a seat, and its chain has a person at its root.
func wakeCrew(t *testing.T, wakes string, prov http.HandlerFunc) (*Workspace, talkootSeat, *stepClock) {
	t.Helper()
	cwd := talkootHome(t)
	wakesConfig(t, wakes)
	clock := &stepClock{now: time.Now()}
	w := wakeDaemon(t, cwd, clock, prov)
	if _, err := w.talkootCreate(t.Context(), "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	return w, helmAfterOneTurn(t, w), clock
}

func helmAfterOneTurn(t *testing.T, w *Workspace) talkootSeat {
	t.Helper()
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"helm"}, "Ship the import, and watch its CI run.", nil, ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm's first turn", func() bool {
		m := memberView(t, w, "crew", "helm")
		return m.Status.Turns == 1 && !m.Status.Working
	})
	seat, ok := w.talkootSeatOf(memberView(t, w, "crew", "helm").Session)
	if !ok {
		t.Fatal("helm holds no seat")
	}
	return seat
}

func wakeLines(t *testing.T) []talkoot.Line {
	t.Helper()
	return roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineWake })
}

func wakeEnvelopes(t *testing.T) []talkoot.Line {
	t.Helper()
	return roomLines(t, "crew", func(l talkoot.Line) bool {
		return l.Type == talkoot.LineEnvelope && l.Envelope != nil && l.Envelope.Kind == talkoot.KindWake
	})
}

func wakeTimers(w *Workspace) []timers.Info {
	var out []timers.Info
	if svc := w.Timers(); svc != nil {
		for _, ti := range svc.List() {
			if ti.Owner == wakeTimerOwner {
				out = append(out, ti)
			}
		}
	}
	return out
}

func helmTurns(t *testing.T, w *Workspace) int {
	t.Helper()
	return memberView(t, w, "crew", "helm").Status.Turns
}

// The whole path: a member sets a wake, the timer keeps it, and when it is
// due the member's session gets one short text and a turn.
func TestAMemberSetsAWakeAndItStartsATurn(t *testing.T) {
	w, _, clock := wakeCrew(t, `{"per_member": 2, "max_delay": "2h"}`, okProvider)
	ctx := t.Context()
	room, err := w.Subscribe(ctx, ctrlproto.TalkootAddr("crew"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := w.resolve(memberView(t, w, "crew", "helm").Session)
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := s.agent.LookupTool("talkoot_wake")
	if !ok {
		t.Fatal("a native member on a daemon with wakes on has no talkoot_wake")
	}
	res, err := tool.Execute(ctx, []byte(`{"delay_minutes": 10, "reason": "Check the CI run of the import."}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	wk, ok := res.Details.(talkoot.Wake)
	if !ok || wk.ID == "" || wk.Member != "helm" {
		t.Fatalf("the tool's result must hold the wake, got %+v", res.Details)
	}
	if text := res.Content[0].(provider.TextBlock).Text; !strings.Contains(text, "1 of 2 open wakes") {
		t.Errorf("the result must count the open wakes against the cap, got %q", text)
	}

	// What the set leaves: one timer, one line, one event, and the view.
	if ts := wakeTimers(w); len(ts) != 1 || ts[0].ID != wk.ID || !ts[0].Schedule.At.Equal(wk.Due) || ts[0].Problem != "" {
		t.Fatalf("the timer service must hold the wake with a handler: %+v", ts)
	}
	if ls := wakeLines(t); len(ls) != 1 || ls[0].Action != talkoot.WakeSet || ls[0].Ref != wk.ID || !ls[0].Due.Equal(wk.Due) {
		t.Fatalf("the room must record the wake when it is set: %+v", ls)
	}
	if l := nextEvent(t, room, ctrlproto.EventTalkootWake).Talkoot.Line; l == nil || l.Action != talkoot.WakeSet || l.Ref != wk.ID || !l.Due.Equal(wk.Due) || l.Text != wk.Reason {
		t.Errorf("the talkoot_wake event carried %+v", l)
	}
	view, err := w.Talkoot(ctx, ctrlproto.TalkootRef{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	if v := view.Wakes; v == nil || !v.On || v.PerMember != 2 || v.MaxDelay != "2h" || len(v.Open) != 1 ||
		v.Open[0].ID != wk.ID || v.Open[0].Member != "helm" || v.Open[0].Reason != wk.Reason || !v.Open[0].Due.Equal(wk.Due) {
		t.Fatalf("the view must show that wakes are on, the caps, and the open wake: %+v", v)
	}
	if n := helmTurns(t, w); n != 1 {
		t.Fatalf("a wake that is set starts no turn, got %d turns", n)
	}

	// Not yet due.
	clock.advance(5 * time.Minute)
	w.Timers().Poll()
	if len(wakeEnvelopes(t)) != 0 {
		t.Fatal("the wake fired before its time")
	}

	clock.advance(5*time.Minute + 5*time.Second)
	w.Timers().Poll()
	waitTalkoot(t, "the wake's turn", func() bool {
		m := memberView(t, w, "crew", "helm")
		return m.Status.Turns == 2 && !m.Status.Working
	})
	envs := wakeEnvelopes(t)
	if len(envs) != 1 || envs[0].Envelope.ID != wk.ID || envs[0].Envelope.From != "helm" || envs[0].Envelope.Body != wk.Reason {
		t.Fatalf("the firing is one envelope with the wake's id and the reason alone: %+v", envs)
	}
	// 🔑 The text is a pointer. The member reads it as text that terva sent,
	// under the source of a team's delivery, and never as a person's words.
	var got []provider.Message
	for _, m := range s.agent.Messages() {
		if m.Role != provider.RoleUser {
			continue
		}
		for _, c := range m.Content {
			if tb, ok := c.(provider.TextBlock); ok && strings.Contains(tb.Text, "wake that you set") {
				got = append(got, m)
				if len(tb.Text) > 600 {
					t.Errorf("the wake's text must stay a pointer, got %d bytes", len(tb.Text))
				}
				for _, want := range []string{"> Check the CI run of the import.", "no person did", wk.ID} {
					if !strings.Contains(tb.Text, want) {
						t.Errorf("the member must read %q, got:\n%s", want, tb.Text)
					}
				}
			}
		}
	}
	if len(got) != 1 {
		t.Fatalf("the member's transcript must hold the wake once, got %d", len(got))
	}
	if src := got[0].Meta[core.MetaSource]; src != ctrlproto.QueueSourceTalkootDelivery {
		t.Errorf("the wake must enter the transcript as runtime text, got the source %q", src)
	}
	if ts := wakeTimers(w); len(ts) != 0 {
		t.Errorf("a wake that fired leaves no timer: %+v", ts)
	}
	view, err = w.Talkoot(ctx, ctrlproto.TalkootRef{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	if view.Wakes == nil || len(view.Wakes.Open) != 0 {
		t.Errorf("a wake that fired is no longer open: %+v", view.Wakes)
	}
}

// 🚨 The tool is the authority, so it exists only where a person turned wakes
// on. A daemon with no key gives its members no such tool.
func TestAMemberHasNoWakeToolUnlessThePersonTurnedWakesOn(t *testing.T) {
	for _, tc := range []struct {
		name, wakes string
		want        bool
	}{
		{"no key", "", false},
		{"a count of zero", `{"per_member": 0}`, false},
		{"a delay that does not parse", `{"per_member": 2, "max_delay": "2 days"}`, false},
		{"on", `{"per_member": 1}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, seat, _ := wakeCrew(t, tc.wakes, okProvider)
			s, err := w.resolve(memberView(t, w, "crew", "helm").Session)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := s.agent.LookupTool("talkoot_send"); !ok {
				t.Fatal("the positive control: helm has no talkoot_send")
			}
			if _, ok := s.agent.LookupTool("talkoot_wake"); ok != tc.want {
				t.Errorf("talkoot_wake present = %v, want %v", ok, tc.want)
			}
			// The seat refuses too, for a session that kept an older tool.
			_, err = seat.Wake(time.Minute, "Check it.")
			if tc.want && err != nil {
				t.Errorf("a wake on a daemon with wakes on was refused: %v", err)
			}
			if !tc.want && (err == nil || !strings.Contains(err.Error(), "wakes are off") || !strings.Contains(err.Error(), "talkoot_wakes")) {
				t.Errorf("the seat must say that wakes are off and name the key, got %v", err)
			}
			view, err := w.Talkoot(t.Context(), ctrlproto.TalkootRef{ID: "crew"})
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tc.wakes == "" && view.Wakes != nil:
				t.Errorf("a daemon with no key shows no wake state, got %+v", view.Wakes)
			case tc.wakes != "" && (view.Wakes == nil || view.Wakes.On != tc.want):
				t.Errorf("the view must say whether wakes are on: %+v", view.Wakes)
			case tc.wakes != "" && !tc.want && view.Wakes.Problem == "":
				t.Error("a key that turns nothing on must say why in the view")
			}
		})
	}
}

// A daemon that keeps no timers cannot keep a wake. The member still has the
// tool, because the tool is attached before a daemon's timers start, and the
// seat refuses with the reason.
func TestAHostWithNoTimersRefusesAWake(t *testing.T) {
	cwd := talkootHome(t)
	wakesConfig(t, `{"per_member": 2}`)
	w := openTalkootWorkspaceWith(t, cwd, okProvider)
	w.LoadTalkoots()
	if _, err := w.talkootCreate(t.Context(), "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	helmAfterOneTurn(t, w)
	s, err := w.resolve(memberView(t, w, "crew", "helm").Session)
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := s.agent.LookupTool("talkoot_wake")
	if !ok {
		t.Fatal("a member on a daemon with wakes on has no talkoot_wake")
	}
	if _, err := tool.Execute(t.Context(), []byte(`{"delay_minutes": 1, "reason": "Check it."}`), nil); err == nil || !strings.Contains(err.Error(), "no timer service") {
		t.Errorf("the tool must say that the host keeps no timers, got %v", err)
	}
	if len(wakeLines(t)) != 0 {
		t.Error("a refused wake left a line in the room")
	}
	view, err := w.Talkoot(t.Context(), ctrlproto.TalkootRef{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	if view.Wakes == nil || view.Wakes.On || !strings.Contains(view.Wakes.Problem, "no timer service") {
		t.Errorf("the view must say why wakes do not work here: %+v", view.Wakes)
	}
}

// 🚨 A daemon starts its talkoots before its timers. A delivery that waited
// for the restart builds its member's session in between, and that session
// must still get the tool, or a restart would be how a member loses wakes.
func TestAMemberWhoseSessionWasBuiltBeforeTheTimersCanSetAWake(t *testing.T) {
	cwd := talkootHome(t)
	wakesConfig(t, `{"per_member": 2}`)
	clock := &stepClock{now: time.Now()}
	w := openTalkootWorkspaceWith(t, cwd, okProvider)
	w.timers.clock = clock
	w.LoadTalkoots()
	if _, err := w.talkootCreate(t.Context(), "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	helmAfterOneTurn(t, w)
	s, err := w.resolve(memberView(t, w, "crew", "helm").Session)
	if err != nil {
		t.Fatal(err)
	}
	if w.Timers() != nil {
		t.Fatal("the positive control: the timers must not run yet")
	}
	w.LoadTimers()
	tool, ok := s.agent.LookupTool("talkoot_wake")
	if !ok {
		t.Fatal("a session built before the timers started has no talkoot_wake")
	}
	if _, err := tool.Execute(t.Context(), []byte(`{"delay_minutes": 1, "reason": "Check it."}`), nil); err != nil {
		t.Fatalf("the tool of a session built before the timers must work once they run: %v", err)
	}
	clock.advance(time.Minute + 5*time.Second)
	w.Timers().Poll()
	waitTalkoot(t, "the wake to fire", func() bool { return len(wakeEnvelopes(t)) == 1 })
}

// talkoot_wake is no seat tool. A roster's tools list narrows it, as it
// narrows any tool of a job, and the seat tools stay.
func TestARostersToolsListNarrowsTheWakeTool(t *testing.T) {
	for _, tc := range []struct {
		name, tools string
		want        bool
	}{
		{"a list without it", "    tools: [read]\n", false},
		{"a list with it", "    tools: [read, talkoot_wake]\n", true},
		{"no list", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := talkootHome(t)
			wakesConfig(t, `{"per_member": 2}`)
			w := wakeDaemon(t, cwd, &stepClock{now: time.Now()}, okProvider)
			text := "---\nname: crew\nhome: " + cwd + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n" + tc.tools + "---\n"
			if _, err := w.talkootCreate(t.Context(), "crew", []byte(text)); err != nil {
				t.Fatal(err)
			}
			helmAfterOneTurn(t, w)
			s, err := w.resolve(memberView(t, w, "crew", "helm").Session)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := s.agent.LookupTool("talkoot_send"); !ok {
				t.Fatal("the positive control: a tools list removed a seat tool")
			}
			if _, ok := s.agent.LookupTool("talkoot_wake"); ok != tc.want {
				t.Errorf("talkoot_wake present = %v, want %v", ok, tc.want)
			}
		})
	}
}

func TestAWakeBeyondACapIsRefusedWithTheCapNamed(t *testing.T) {
	w, seat, _ := wakeCrew(t, `{"per_member": 2, "max_delay": "90m"}`, okProvider)

	_, err := seat.Wake(91*time.Minute, "Check the nightly run.")
	if err == nil || !strings.Contains(err.Error(), "at most 1h30m") || !strings.Contains(err.Error(), "talkoot_wakes.max_delay") {
		t.Fatalf("a delay above the cap must be refused with the cap and its key, got %v", err)
	}
	if len(wakeLines(t)) != 0 || len(wakeTimers(w)) != 0 {
		t.Fatal("a refused wake must leave no line and no timer")
	}
	if _, err := seat.Wake(90*time.Minute, "Check the nightly run."); err != nil {
		t.Fatalf("a delay at the cap is allowed, got %v", err)
	}
	r, err := seat.Wake(10*time.Minute, "Check the deploy.")
	if err != nil || r.Open != 2 || r.Cap != 2 {
		t.Fatalf("the second wake: %+v, %v", r, err)
	}
	_, err = seat.Wake(5*time.Minute, "Check the queue.")
	if err == nil || !strings.Contains(err.Error(), "2 open wake(s), and the limit is 2") || !strings.Contains(err.Error(), "talkoot_wakes.per_member") {
		t.Fatalf("a wake above the count must be refused with the cap and its key, got %v", err)
	}
	// The member set the later wake first. The refusal names the one that
	// fires first, and the view lists that one first.
	if soonest := r.Wake.Due.UTC().Format("2006-01-02 15:04 UTC"); !strings.Contains(err.Error(), "fires at "+soonest) {
		t.Errorf("the refusal must name the wake that fires first, at %s, got %v", soonest, err)
	}
	view, verr := w.Talkoot(t.Context(), ctrlproto.TalkootRef{ID: "crew"})
	if verr != nil {
		t.Fatal(verr)
	}
	if o := view.Wakes.Open; len(o) != 2 || o[0].ID != r.Wake.ID || !o[0].Due.Before(o[1].Due) {
		t.Errorf("the view must list the open wakes the soonest first: %+v", o)
	}
	if n := len(wakeTimers(w)); n != 2 {
		t.Errorf("the refused wake must set no timer, got %d timers", n)
	}
	if n := len(wakeLines(t)); n != 2 {
		t.Errorf("the refused wake must leave no line, got %d lines", n)
	}
}

// Two calls in one model step must not both read a count below the cap.
func TestTwoWakesAtOnceCannotBothPassTheCap(t *testing.T) {
	w, seat, _ := wakeCrew(t, `{"per_member": 1}`, okProvider)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = seat.Wake(time.Hour, fmt.Sprintf("Check run %d.", i))
		}()
	}
	wg.Wait()
	passed := 0
	for _, err := range errs {
		if err == nil {
			passed++
		}
	}
	if passed != 1 || len(wakeTimers(w)) != 1 {
		t.Fatalf("a cap of 1 let %d wakes pass, with %d timers", passed, len(wakeTimers(w)))
	}
}

// 🚨 A person who turns wakes off must get no turn from a wake that a member
// set before. The firing reads the configuration again.
func TestAWakeDoesNotFireAfterThePersonTurnedWakesOff(t *testing.T) {
	w, seat, clock := wakeCrew(t, `{"per_member": 2}`, okProvider)
	r, err := seat.Wake(10*time.Minute, "Check the deploy.")
	if err != nil {
		t.Fatal(err)
	}
	wakesConfig(t, "")
	clock.advance(11 * time.Minute)
	w.Timers().Poll()
	waitTalkoot(t, "the dropped line", func() bool { return len(wakeLines(t)) == 2 })
	ls := wakeLines(t)
	if ls[1].Action != talkoot.WakeDropped || ls[1].Ref != r.Wake.ID || !strings.Contains(ls[1].Reason, "wakes are off") {
		t.Fatalf("the room must say why the wake did not fire: %+v", ls[1])
	}
	if len(wakeEnvelopes(t)) != 0 || helmTurns(t, w) != 1 {
		t.Error("a wake fired after the person turned wakes off")
	}
	if len(wakeTimers(w)) != 0 {
		t.Error("the dropped wake left its timer")
	}
}

func TestAPersonCancelsAnOpenWake(t *testing.T) {
	w, seat, clock := wakeCrew(t, `{"per_member": 2}`, okProvider)
	ctx := t.Context()
	r, err := seat.Wake(10*time.Minute, "Check the deploy.")
	if err != nil {
		t.Fatal(err)
	}
	keep, err := seat.Wake(20*time.Minute, "Check the import.")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.CancelTalkootWake(ctx, ctrlproto.TalkootWakeCancelParams{ID: "crew", By: "sothr", Wake: "01NOSUCHWAKE"}); talkootCode(err) != ctrlproto.CodeNotFound {
		t.Fatalf("a cancel of no wake: %v", err)
	}
	if err := w.CancelTalkootWake(ctx, ctrlproto.TalkootWakeCancelParams{ID: "crew", By: "not a name!", Wake: r.Wake.ID}); err == nil {
		t.Fatal("a cancel with no valid person was allowed")
	}
	if n := len(wakeTimers(w)); n != 2 {
		t.Fatalf("a refused cancel must leave both wakes, got %d", n)
	}
	if err := w.CancelTalkootWake(ctx, ctrlproto.TalkootWakeCancelParams{ID: "crew", By: "sothr", Wake: r.Wake.ID}); err != nil {
		t.Fatal(err)
	}
	ls := wakeLines(t)
	if len(ls) != 3 || ls[2].Action != talkoot.WakeCancelled || ls[2].By != "human:sothr" || ls[2].Ref != r.Wake.ID {
		t.Fatalf("the room must name the person who cancelled: %+v", ls)
	}
	if ts := wakeTimers(w); len(ts) != 1 || ts[0].ID != keep.Wake.ID {
		t.Fatalf("the cancel must remove that wake's timer alone: %+v", ts)
	}
	if err := w.CancelTalkootWake(ctx, ctrlproto.TalkootWakeCancelParams{ID: "crew", By: "sothr", Wake: r.Wake.ID}); talkootCode(err) != ctrlproto.CodeNotFound {
		t.Fatalf("a second cancel: %v", err)
	}
	// The cancel frees a place under the cap.
	if _, err := seat.Wake(30*time.Minute, "Check the queue."); err != nil {
		t.Fatalf("a cancelled wake still counts against the cap: %v", err)
	}
	clock.advance(11 * time.Minute)
	w.Timers().Poll()
	if len(wakeEnvelopes(t)) != 0 || helmTurns(t, w) != 1 {
		t.Error("a cancelled wake fired")
	}
}

// 🚨 TKT-01M7PKC0. A wake that fires while its member is paused writes its
// envelope and waits, and the timer service forgets it. It stays in the list
// of open wakes, it counts toward the member's cap, and a person cancels it
// there. The member gets no turn from a cancelled wake at the resume.
func TestAWakeThatWaitsBehindAPauseShowsCountsAndCanBeCancelled(t *testing.T) {
	w, seat, clock := wakeCrew(t, `{"per_member": 2}`, okProvider)
	ctx := t.Context()
	first, err := seat.Wake(10*time.Minute, "Check the deploy.")
	if err != nil {
		t.Fatal(err)
	}
	second, err := seat.Wake(20*time.Minute, "Check the import.")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.talkootPause(ctx, "crew", "sothr", "helm", "", "wait for me"); err != nil {
		t.Fatal(err)
	}
	open := func() []ctrlproto.TalkootWake {
		t.Helper()
		view, err := w.Talkoot(ctx, ctrlproto.TalkootRef{ID: "crew"})
		if err != nil {
			t.Fatal(err)
		}
		return view.Wakes.Open
	}

	// The first wake fires into the pause.
	clock.advance(11 * time.Minute)
	waitTalkoot(t, "the first wake's firing", func() bool {
		w.Timers().Poll()
		return len(wakeEnvelopes(t)) == 1 && len(wakeTimers(w)) == 1
	})
	o := open()
	if len(o) != 2 || o[0].ID != first.Wake.ID || !o[0].Held || o[0].Reason != "Check the deploy." || o[1].ID != second.Wake.ID || o[1].Held {
		t.Fatalf("the list must hold the wake that waits, first and marked, and the one with a timer: %+v", o)
	}
	_, err = seat.Wake(5*time.Minute, "Check the queue.")
	if err == nil || !strings.Contains(err.Error(), "2 open wake(s), and the limit is 2") {
		t.Fatalf("a wake that waits behind a pause must count toward the cap, got %v", err)
	}
	if at := second.Wake.Due.UTC().Format("2006-01-02 15:04 UTC"); !strings.Contains(err.Error(), "fires at "+at) {
		t.Errorf("the refusal must name the wake that a timer still holds, at %s, got %v", at, err)
	}

	// The second wake fires into the pause too. No wake has a time now.
	clock.advance(10 * time.Minute)
	waitTalkoot(t, "the second wake's firing", func() bool {
		w.Timers().Poll()
		return len(wakeEnvelopes(t)) == 2 && len(wakeTimers(w)) == 0
	})
	_, err = seat.Wake(5*time.Minute, "Check the queue.")
	if err == nil || !strings.Contains(err.Error(), "2 open wake(s), and the limit is 2") || !strings.Contains(err.Error(), "waits behind a pause") {
		t.Fatalf("the refusal must say that each wake waits behind a pause, got %v", err)
	}

	if err := w.CancelTalkootWake(ctx, ctrlproto.TalkootWakeCancelParams{ID: "crew", By: "sothr", Wake: first.Wake.ID}); err != nil {
		t.Fatalf("a person cancels a wake that waits behind a pause: %v", err)
	}
	ls := wakeLines(t)
	if last := ls[len(ls)-1]; last.Action != talkoot.WakeCancelled || last.By != "human:sothr" || last.Ref != first.Wake.ID {
		t.Fatalf("the room must name the person who cancelled: %+v", last)
	}
	if o := open(); len(o) != 1 || o[0].ID != second.Wake.ID || !o[0].Held {
		t.Fatalf("the cancel must take that one wake out of the list: %+v", o)
	}
	if err := w.CancelTalkootWake(ctx, ctrlproto.TalkootWakeCancelParams{ID: "crew", By: "sothr", Wake: first.Wake.ID}); talkootCode(err) != ctrlproto.CodeNotFound {
		t.Fatalf("a second cancel: %v", err)
	}
	if helmTurns(t, w) != 1 {
		t.Fatal("a paused member got a turn")
	}

	if err := w.talkootResume(ctx, "crew", "sothr", "helm", ""); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "the turn of the wake that was kept", func() bool {
		m := memberView(t, w, "crew", "helm")
		return m.Status.Turns == 2 && !m.Status.Working
	})
	settleRuns(t, w)
	// The room has a delivery line for each envelope that reached a member.
	delivered := map[string]bool{}
	for _, l := range roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineDelivery }) {
		delivered[l.Ref] = true
	}
	if !delivered[second.Wake.ID] {
		t.Error("the positive control: the room has no delivery of the wake that was kept")
	}
	if delivered[first.Wake.ID] {
		t.Error("the member got a turn from the wake that the person cancelled")
	}
	if n := helmTurns(t, w); n != 2 {
		t.Errorf("the resume must give the member one turn, got %d turns in all", n)
	}
	if o := open(); len(o) != 0 {
		t.Errorf("a wake that reached its member is open no longer: %+v", o)
	}
}

// The timer service keeps a wake across a restart. A wake that came due while
// the daemon was down fires at the next start, and says that it is late.
func TestAWakeFiresAfterARestartAndSaysItIsLate(t *testing.T) {
	cwd := talkootHome(t)
	wakesConfig(t, `{"per_member": 2}`)
	clock := &stepClock{now: time.Now()}
	w := wakeDaemon(t, cwd, clock, okProvider)
	if _, err := w.talkootCreate(t.Context(), "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	seat := helmAfterOneTurn(t, w)
	r, err := seat.Wake(10*time.Minute, "Check the nightly run.")
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	clock.advance(3 * time.Hour)
	w2 := wakeDaemon(t, cwd, clock, okProvider)
	w2.Timers().Poll()
	waitTalkoot(t, "the wake after the restart", func() bool { return len(wakeEnvelopes(t)) == 1 })
	e := wakeEnvelopes(t)[0].Envelope
	if e.ID != r.Wake.ID || !strings.Contains(e.Body, "Check the nightly run.") || !strings.Contains(e.Body, "3 hours late") {
		t.Fatalf("the late wake must say how late it is: %+v", e)
	}
	waitTalkoot(t, "the wake's turn", func() bool {
		m := memberView(t, w2, "crew", "helm")
		return m.Status.Turns == 2 && !m.Status.Working
	})
	w2.Timers().Poll()
	if n := len(wakeEnvelopes(t)); n != 1 {
		t.Errorf("a one-time wake fires once, got %d envelopes", n)
	}
}

// A host may start its timers before its talkoots. A wake that came due then
// waits for the teams, and its timer says that it waits.
func TestAWakeWaitsUntilTheTalkootsRun(t *testing.T) {
	cwd := talkootHome(t)
	wakesConfig(t, `{"per_member": 2}`)
	clock := &stepClock{now: time.Now()}
	w := wakeDaemon(t, cwd, clock, okProvider)
	if _, err := w.talkootCreate(t.Context(), "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	seat := helmAfterOneTurn(t, w)
	if _, err := seat.Wake(10*time.Minute, "Check the nightly run."); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	clock.advance(time.Hour)
	w2 := openTalkootWorkspaceWith(t, cwd, okProvider)
	w2.timers.clock = clock
	w2.LoadTimers()
	w2.Timers().Poll()
	if ts := wakeTimers(w2); len(ts) != 1 || ts[0].Problem == "" {
		t.Fatalf("a wake with no team to fire into must wait, and say so: %+v", ts)
	}
	if len(wakeEnvelopes(t)) != 0 {
		t.Fatal("a wake fired before its team ran")
	}
	w2.LoadTalkoots()
	w2.Timers().Poll()
	waitTalkoot(t, "the wake once the team runs", func() bool { return len(wakeEnvelopes(t)) == 1 })
}

// breakRoster makes the crew's roster a file that does not read, so the next
// daemon does not start the team. It returns the call that puts it back.
func breakRoster(t *testing.T) (restore func()) {
	t.Helper()
	path := filepath.Join(talkoot.Dir(), "crew", talkoot.FileName)
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{ this is no roster"), 0o600); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if err := os.WriteFile(path, good, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// waitKept waits until the timer service holds the one wake again, due one
// wait of a team from now.
//
// ⚠️ The service polls on its own when it starts, so the firing may still be
// in flight when a Poll of the test returns. Read the timers in a wait.
func waitKept(t *testing.T, w *Workspace, clock *stepClock, what string) timers.Info {
	t.Helper()
	var kept timers.Info
	waitTalkoot(t, what, func() bool {
		w.Timers().Poll()
		ts := wakeTimers(w)
		if len(ts) != 1 || !ts[0].Next.Equal(clock.Now().Add(wakeTeamRetry)) {
			return false
		}
		kept = ts[0]
		return true
	})
	return kept
}

// 🚨 A wake that comes due while the daemon does not run its team is not
// lost. No room is open to write, so the timer holds it, and the list of
// teams says that it waits. It fires when a daemon runs the team again, and
// it says how late it is.
func TestAWakeWaitsForATeamThatTheDaemonDoesNotRun(t *testing.T) {
	cwd := talkootHome(t)
	wakesConfig(t, `{"per_member": 2}`)
	clock := &stepClock{now: time.Now()}
	w := wakeDaemon(t, cwd, clock, okProvider)
	if _, err := w.talkootCreate(t.Context(), "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	seat := helmAfterOneTurn(t, w)
	r, err := seat.Wake(10*time.Minute, "Check the nightly run.")
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	restore := breakRoster(t)
	clock.advance(3*time.Hour + 10*time.Minute)
	w2 := wakeDaemon(t, cwd, clock, okProvider)
	if _, err := w2.talkootRunOf("crew"); !errors.Is(err, ErrTalkootNotHere) {
		t.Fatalf("the daemon must not run a team whose roster does not read, got %v", err)
	}
	// Two firings: the wake stays at each one.
	if kept := waitKept(t, w2, clock, "the wake's first wait for its team"); kept.ID != r.Wake.ID {
		t.Fatalf("the timer must hold the wake that the member set: %+v", kept)
	}
	clock.advance(wakeTeamRetry)
	waitKept(t, w2, clock, "the wake's second wait for its team")
	if n := len(wakeEnvelopes(t)); n != 0 {
		t.Fatalf("a wake fired into a team that the daemon does not run: %d", n)
	}
	list, err := w2.talkootList(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	said := false
	for _, s := range list {
		if s.ID == "crew" {
			said = !s.Running && strings.Contains(s.Problem, "1 wake(s) came due, and wait until the team runs")
		}
	}
	if !said {
		t.Errorf("the list of teams must say that a wake waits for the team: %+v", list)
	}
	_ = w2.Close()

	// The wake was due 3 hours and 5 minutes ago. 55 minutes more make 4 hours.
	restore()
	clock.advance(55 * time.Minute)
	w3 := wakeDaemon(t, cwd, clock, okProvider)
	w3.Timers().Poll()
	waitTalkoot(t, "the wake once a daemon runs the team", func() bool { return len(wakeEnvelopes(t)) == 1 })
	e := wakeEnvelopes(t)[0].Envelope
	if e.ID != r.Wake.ID || !strings.Contains(e.Body, "Check the nightly run.") || !strings.Contains(e.Body, "4 hours late, because the daemon did not run the team") {
		t.Fatalf("the wake must say how long it waited for its team: %+v", e)
	}
	waitTalkoot(t, "the wake's turn", func() bool {
		m := memberView(t, w3, "crew", "helm")
		return m.Status.Turns == 2 && !m.Status.Working
	})
	if ts := wakeTimers(w3); len(ts) != 0 {
		t.Errorf("a wake that fired must leave no timer: %+v", ts)
	}
	if list, err := w3.talkootList(t.Context()); err != nil || len(list) != 1 || list[0].Problem != "" {
		t.Errorf("a team that runs has no problem, and no wake waits for it: %+v %v", list, err)
	}
}

// The wait has two ends. Without them a wake of a team that never runs here
// again would fire for ever, and no person can cancel it.
func TestAWakeStopsWaitingForItsTeamAtTheEndOfTheWait(t *testing.T) {
	for name, end := range map[string]func(t *testing.T, clock *stepClock){
		"after seven days": func(_ *testing.T, clock *stepClock) { clock.advance(wakeTeamWait + time.Hour) },
		"when the team is gone": func(t *testing.T, _ *stepClock) {
			if err := os.Remove(filepath.Join(talkoot.Dir(), "crew", talkoot.FileName)); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			cwd := talkootHome(t)
			wakesConfig(t, `{"per_member": 2}`)
			clock := &stepClock{now: time.Now()}
			w := wakeDaemon(t, cwd, clock, okProvider)
			if _, err := w.talkootCreate(t.Context(), "crew", crewText(cwd)); err != nil {
				t.Fatal(err)
			}
			seat := helmAfterOneTurn(t, w)
			if _, err := seat.Wake(10*time.Minute, "Check the nightly run."); err != nil {
				t.Fatal(err)
			}
			_ = w.Close()

			breakRoster(t)
			clock.advance(time.Hour)
			w2 := wakeDaemon(t, cwd, clock, okProvider)
			// The positive control: the wake waits before the end comes.
			waitKept(t, w2, clock, "the wake's wait before its end")
			end(t, clock)
			clock.advance(wakeTeamRetry)
			waitTalkoot(t, "the end of the wake's wait", func() bool {
				w2.Timers().Poll()
				return len(wakeTimers(w2)) == 0
			})
		})
	}
}

// gateFirst blocks the first model request until release closes, and answers
// every request with a short reply.
func gateFirst(entered chan<- struct{}, release <-chan struct{}) http.HandlerFunc {
	var once sync.Once
	return func(rw http.ResponseWriter, r *http.Request) {
		once.Do(func() {
			entered <- struct{}{}
			<-release
		})
		okProvider(rw, r)
	}
}

// 🚨 A wake never interrupts a member at work. Its text waits in the session's
// queue, and the member reads it at the next boundary of its turn.
func TestAWakeForAWorkingMemberWaitsInItsQueue(t *testing.T) {
	cwd := talkootHome(t)
	wakesConfig(t, `{"per_member": 2}`)
	clock := &stepClock{now: time.Now()}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	// Registered before the daemon, so it runs after the daemon's close has
	// begun and a blocked request cannot hold the close.
	t.Cleanup(free)
	w := wakeDaemon(t, cwd, clock, gateFirst(entered, release))
	if _, err := w.talkootCreate(t.Context(), "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPost(t.Context(), "crew", "sothr", []string{"helm"}, "Ship the import.", nil, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("helm's turn never reached the provider")
	}
	sid := memberView(t, w, "crew", "helm").Session
	seat, ok := w.talkootSeatOf(sid)
	if !ok {
		t.Fatal("helm holds no seat")
	}
	// The member is in its turn, as it is when the tool runs.
	r, err := seat.Wake(time.Minute, "Check the import.")
	if err != nil {
		t.Fatal(err)
	}
	s, err := w.resolve(sid)
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Minute + 5*time.Second)
	w.Timers().Poll()
	waitTalkoot(t, "the wake in helm's queue", func() bool {
		for _, q := range s.agent.PendingQueuedMessages() {
			if q.Source == ctrlproto.QueueSourceTalkootDelivery && strings.Contains(q.Text, "wake that you set") {
				return true
			}
		}
		return false
	})
	if !memberView(t, w, "crew", "helm").Status.Working {
		t.Fatal("the wake ended the turn that was running")
	}
	if n := helmTurns(t, w); n != 0 {
		t.Fatalf("no turn has ended yet, got %d", n)
	}
	read := func() int {
		return len(roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineRead && l.Ref == r.Wake.ID }))
	}
	if read() != 0 {
		t.Fatal("the member read the wake while its model call was still running")
	}
	free()
	waitTalkoot(t, "helm to read the wake", func() bool { return read() == 1 })
	waitTalkoot(t, "helm to finish", func() bool { return !memberView(t, w, "crew", "helm").Status.Working })
}

// Decision 0023, as amended on 2026-11-19, puts the wake on the MCP bridge. A
// worker's seat is a waker, as a native seat is. The tests of what its wake
// does are in workspace_talkoot_worker_wake_test.go.
func TestAWorkerSeatIsAWaker(t *testing.T) {
	var seat any = workerSeat{}
	if _, ok := seat.(tools.TalkootWaker); !ok {
		t.Fatal("a worker's seat cannot set a wake, and decision 0023 puts that verb on the bridge")
	}
	var native any = talkootSeat{}
	if _, ok := native.(tools.TalkootWaker); !ok {
		t.Fatal("a native seat is no waker")
	}
}

func wakeTries(t *testing.T, ti timers.Info) int {
	t.Helper()
	var d wakeData
	if err := json.Unmarshal(ti.Data, &d); err != nil {
		t.Fatal(err)
	}
	return d.Tries
}

// pollWakes runs the timers at the clock's time, waits for ok, and reports
// whether it came. It never fails the test, so a caller that holds the run's
// lock can let go of it first.
//
// 🔑 The service's own loop reads the same clock, and a firing or a new timer
// sets it going. So the loop can take a due timer before this Poll does. The
// handler then runs on the loop's goroutine, and Poll returns before the
// handler ends. A test must wait for what the handler writes, and must not
// read it straight after Poll.
func pollWakes(w *Workspace, ok func() bool) bool {
	w.Timers().Poll()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return true
		}
	}
	return false
}

// wakeTriesAre reports whether the daemon holds one wake timer that counts n
// tries.
func wakeTriesAre(t *testing.T, w *Workspace, n int) func() bool {
	return func() bool {
		ts := wakeTimers(w)
		return len(ts) == 1 && wakeTries(t, ts[0]) == n
	}
}

// 🚨 The timer service forgets a one-time timer when its handler returns. A
// handler that gave the timer up before the room held the firing would lose
// the wake in a daemon that stopped just then. So a firing that the room does
// not take keeps its timer, and is tried again.
func TestAWakeKeepsItsTimerUntilTheRoomHoldsTheFiring(t *testing.T) {
	w, seat, clock := wakeCrew(t, `{"per_member": 2}`, okProvider)
	r, err := seat.Wake(10*time.Minute, "Check the deploy.")
	if err != nil {
		t.Fatal(err)
	}
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	// A roster update holds the run's lock for its whole apply.
	run.mu.Lock()
	clock.advance(10*time.Minute + 5*time.Second)
	pollWakes(w, wakeTriesAre(t, w, 1))
	ts := wakeTimers(w)
	run.mu.Unlock()
	if len(ts) != 1 || ts[0].ID != r.Wake.ID || wakeTries(t, ts[0]) != 1 || !ts[0].Schedule.At.After(r.Wake.Due) {
		t.Fatalf("a firing that the room did not take must keep its timer, for a later time: %+v", ts)
	}
	if len(wakeEnvelopes(t)) != 0 {
		t.Fatal("the wake fired while the team was not free")
	}
	// The app still shows the time the member asked for.
	view, err := w.Talkoot(t.Context(), ctrlproto.TalkootRef{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	if o := view.Wakes.Open; len(o) != 1 || !o[0].Due.Equal(r.Wake.Due) {
		t.Errorf("a wake that is tried again keeps its due time in the view: %+v", o)
	}

	// Two more tries find the team still not free. The wake is then more
	// than a minute past its time, and the daemon was at work for all of it.
	run.mu.Lock()
	for i := range 2 {
		clock.advance(wakeRetryAfter + time.Second)
		pollWakes(w, wakeTriesAre(t, w, i+2))
	}
	ts = wakeTimers(w)
	run.mu.Unlock()
	if len(ts) != 1 || wakeTries(t, ts[0]) != 3 {
		t.Fatalf("each try that the room did not take must count: %+v", ts)
	}

	clock.advance(wakeRetryAfter + time.Second)
	w.Timers().Poll()
	waitTalkoot(t, "the wake on its fourth try", func() bool { return len(wakeEnvelopes(t)) == 1 })
	e := wakeEnvelopes(t)[0].Envelope
	if e.ID != r.Wake.ID {
		t.Errorf("the last try sends the same wake: %+v", e)
	}
	// 🚨 The late notice tells the member that the daemon was away. The
	// waits between tries are not that.
	if strings.Contains(e.Body, "late") {
		t.Errorf("a wake that waited for the team, in a daemon that was at work, is not late: %q", e.Body)
	}
	if ts := wakeTimers(w); len(ts) != 0 {
		t.Errorf("a wake that fired leaves no timer: %+v", ts)
	}
}

// A room that takes no firing for ten minutes has a fault that a person must
// see to. The wake then stops, and does not try forever.
func TestAWakeThatTheRoomNeverTakesStopsAfterItsTries(t *testing.T) {
	w, seat, clock := wakeCrew(t, `{"per_member": 2}`, okProvider)
	if _, err := seat.Wake(time.Minute, "Check the deploy."); err != nil {
		t.Fatal(err)
	}
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	run.mu.Lock()
	clock.advance(time.Minute + time.Second)
	for i := range wakeMaxTries {
		if !pollWakes(w, wakeTriesAre(t, w, i+1)) {
			ts := wakeTimers(w)
			run.mu.Unlock()
			t.Fatalf("try %d must keep the timer and count: %+v", i+1, ts)
		}
		clock.advance(wakeRetryAfter + time.Second)
	}
	pollWakes(w, func() bool { return len(wakeTimers(w)) == 0 })
	ts := wakeTimers(w)
	if n := len(wakeLines(t)); n != 1 {
		run.mu.Unlock()
		t.Fatalf("the room must hold the set line alone while the team is not free, got %d wake lines", n)
	}
	run.mu.Unlock()
	if len(ts) != 0 {
		t.Fatalf("the wake must stop after %d tries: %+v", wakeMaxTries, ts)
	}
	// 🚨 The timer is gone, so the room is the only record left. It must not
	// show the wake as set for ever.
	waitTalkoot(t, "the drop of the wake, once the team is free", func() bool { return len(wakeLines(t)) == 2 })
	if l := wakeLines(t)[1]; l.Action != talkoot.WakeDropped || !strings.Contains(l.Reason, "tries") {
		t.Errorf("the room must say that the wake was dropped, and why: %+v", l)
	}
	if len(wakeEnvelopes(t)) != 0 {
		t.Error("a wake that was dropped fired")
	}
}

// 🚨 A cancel writes the room first, and the timer's removal can then fail.
// The timer that stays must not keep the wake open: the person cancelled it,
// and the member needs its place under the cap.
func TestACancelledWakeWhoseTimerStayedIsNotOpen(t *testing.T) {
	w, seat, clock := wakeCrew(t, `{"per_member": 1}`, okProvider)
	r, err := seat.Wake(10*time.Minute, "Check the deploy.")
	if err != nil {
		t.Fatal(err)
	}
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	// The first half of a cancel alone, which is what a failed removal of
	// the timer leaves behind.
	if err := run.do(func(rt *talkoot.Router) error { return rt.CancelWake(r.Wake, "human:sothr") }); err != nil {
		t.Fatal(err)
	}
	if ts := wakeTimers(w); len(ts) != 1 {
		t.Fatalf("the timer must still be there, or this test proves nothing: %+v", ts)
	}
	view, err := w.Talkoot(t.Context(), ctrlproto.TalkootRef{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	if view.Wakes == nil || len(view.Wakes.Open) != 0 {
		t.Errorf("the app must not list a wake that the room closed: %+v", view.Wakes)
	}
	if err := w.CancelTalkootWake(t.Context(), ctrlproto.TalkootWakeCancelParams{ID: "crew", By: "sothr", Wake: r.Wake.ID}); talkootCode(err) != ctrlproto.CodeNotFound {
		t.Errorf("a second cancel must say that the wake is gone: %v", err)
	}
	if _, err := seat.Wake(30*time.Minute, "Check the queue."); err != nil {
		t.Fatalf("a cancelled wake still counts against the cap: %v", err)
	}
	// At its time the timer that stayed fires nothing, and goes.
	clock.advance(11 * time.Minute)
	if !pollWakes(w, func() bool { ts := wakeTimers(w); return len(ts) == 1 && ts[0].ID != r.Wake.ID }) {
		t.Errorf("the timer of the cancelled wake must go at its time: %+v", wakeTimers(w))
	}
	if len(wakeEnvelopes(t)) != 0 || helmTurns(t, w) != 1 {
		t.Error("a cancelled wake fired")
	}
}
