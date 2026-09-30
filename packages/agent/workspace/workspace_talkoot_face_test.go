package workspace

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/talkoot/expression"
)

// faceCrew starts the crew on a workspace whose test provider fails every
// turn, and posts to helm, so helm's turn fails.
func faceCrew(t *testing.T, rows []expression.Row) (*Workspace, string) {
	t.Helper()
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	w.talkootFaceRows = rows
	ctx := t.Context()
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.PostTalkoot(ctx, ctrlproto.TalkootPostParams{ID: "crew", By: "sothr", To: []string{"helm"}, Body: "start"}); err != nil {
		t.Fatal(err)
	}
	return w, cwd
}

// watchFace records the crew's status events on the wire, and its beats.
func watchFace(t *testing.T, w *Workspace) (statuses func() []ctrlproto.TalkootMemberStatus, beats func() []ctrlproto.TalkootBeat) {
	t.Helper()
	var mu sync.Mutex
	var st []ctrlproto.TalkootMemberStatus
	var bs []ctrlproto.TalkootBeat
	stop := w.talkootWatch("crew", func(ev talkootEvent) {
		e, ok := wireTalkootEvent(ev)
		if !ok || e.Talkoot == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch e.Type {
		case ctrlproto.EventTalkootStatus:
			for _, m := range e.Talkoot.Members {
				if m.Member == "helm" {
					st = append(st, m)
				}
			}
		case ctrlproto.EventTalkootBeat:
			bs = append(bs, *e.Talkoot.Beat)
		}
	})
	t.Cleanup(stop)
	return func() []ctrlproto.TalkootMemberStatus {
			mu.Lock()
			defer mu.Unlock()
			return append([]ctrlproto.TalkootMemberStatus(nil), st...)
		}, func() []ctrlproto.TalkootBeat {
			mu.Lock()
			defer mu.Unlock()
			return append([]ctrlproto.TalkootBeat(nil), bs...)
		}
}

// A failed turn frowns: helm's status carries the expression and its cause,
// in the view and on the status event a client hears.
func TestAFailedTurnShowsOnTheFace(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))}); err != nil {
		t.Fatal(err)
	}
	statuses, _ := watchFace(t, w)
	if _, err := w.PostTalkoot(ctx, ctrlproto.TalkootPostParams{ID: "crew", By: "sothr", To: []string{"helm"}, Body: "start"}); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm to frown", func() bool { return memberView(t, w, "crew", "helm").Status.Expression == expression.PoseFrustrated })
	if c := memberView(t, w, "crew", "helm").Status.ExpressionCause; !strings.HasPrefix(c, "turn 1 failed, x1 in a row") {
		t.Fatalf("cause = %q", c)
	}
	waitTalkoot(t, "the status event", func() bool {
		st := statuses()
		return len(st) > 0 && st[len(st)-1].Expression == expression.PoseFrustrated && st[len(st)-1].ExpressionCause != ""
	})
}

// A member's message plays a glance toward its recipient, as a talkoot_beat
// event on the room's address.
func TestAMemberSendPlaysAGlance(t *testing.T) {
	w, _ := faceCrew(t, nil)
	_, beats := watchFace(t, w)
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	e := talkoot.Envelope{ID: "e-glance", Talkoot: "crew", From: "helm", To: []string{"jev"}, Kind: talkoot.KindMessage, Body: "Look.", At: time.Now()}
	if err := run.room.Append(talkoot.Line{Type: talkoot.LineEnvelope, At: e.At, Envelope: &e}); err != nil {
		t.Fatal(err)
	}
	run.flush()
	waitTalkoot(t, "the glance", func() bool {
		for _, b := range beats() {
			if b.Member == "helm" && b.Beat == expression.BeatGlance && b.Toward == "jev" && b.Cause != "" {
				return true
			}
		}
		return false
	})
}

// A held pose ends on its own: the run's timer flushes the run when the hold
// runs out, and the status event draws the default pose again.
func TestAHeldPoseEndsOnItsTimer(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	w.talkootFaceRows = []expression.Row{{Signal: expression.SignalTurnFailed, Pose: expression.PoseFrustrated, Hold: 300 * time.Millisecond}}
	ctx := t.Context()
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))}); err != nil {
		t.Fatal(err)
	}
	statuses, _ := watchFace(t, w)
	if _, err := w.PostTalkoot(ctx, ctrlproto.TalkootPostParams{ID: "crew", By: "sothr", To: []string{"helm"}, Body: "start"}); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm to frown", func() bool {
		st := statuses()
		return len(st) > 0 && st[len(st)-1].Expression == expression.PoseFrustrated
	})
	waitTalkoot(t, "the hold to end with no other change", func() bool {
		st := statuses()
		return st[len(st)-1].Expression == ""
	})
	tr, err := w.TalkootTrace(ctx, ctrlproto.TalkootTraceParams{ID: "crew", Member: "helm"})
	if err != nil {
		t.Fatal(err)
	}
	if last := tr.Entries[len(tr.Entries)-1]; last.Signal != "hold ended" || last.Cause == "" {
		t.Fatalf("the trace's last entry = %+v, want the hold's end", last)
	}
}

// A restart replays the room, so helm's face keeps its mood, and the trace
// holds the signals again.
func TestARestartKeepsTheMood(t *testing.T) {
	w, cwd := faceCrew(t, nil)
	waitTalkoot(t, "helm to frown", func() bool { return memberView(t, w, "crew", "helm").Status.Expression == expression.PoseFrustrated })
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w2 := openTalkootWorkspace(t, cwd)
	w2.LoadTalkoots()
	if x := memberView(t, w2, "crew", "helm").Status.Expression; x != expression.PoseFrustrated {
		t.Fatalf("after a restart helm shows %q", x)
	}
	tr, err := w2.TalkootTrace(t.Context(), ctrlproto.TalkootTraceParams{ID: "crew", Member: "helm"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Entries) == 0 {
		t.Fatal("the replay left the trace empty")
	}
}

// talkoot.trace returns the seed and the member's entries, and an unknown
// talkoot is not found.
func TestTalkootTraceReadsTheBuffer(t *testing.T) {
	w, _ := faceCrew(t, nil)
	waitTalkoot(t, "helm to frown", func() bool { return memberView(t, w, "crew", "helm").Status.Expression == expression.PoseFrustrated })
	tr, err := w.TalkootTrace(t.Context(), ctrlproto.TalkootTraceParams{ID: "crew", Member: "helm"})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Seed != faceSeed("crew") || tr.Member != "helm" {
		t.Fatalf("trace = %+v", tr)
	}
	found := false
	for _, e := range tr.Entries {
		if e.Signal == "turn failed" && e.Expression == expression.PoseFrustrated && e.Cause != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("entries = %+v, want the failed turn", tr.Entries)
	}
	if empty, err := w.TalkootTrace(t.Context(), ctrlproto.TalkootTraceParams{ID: "crew", Member: "nobody"}); err != nil || empty.Entries == nil || len(empty.Entries) != 0 {
		t.Fatalf("a member with no trace = %+v, %v", empty, err)
	}
	_, err = w.TalkootTrace(t.Context(), ctrlproto.TalkootTraceParams{ID: "no-such", Member: "helm"})
	var ce *ctrlproto.Error
	if !errors.As(err, &ce) || ce.Code != ctrlproto.CodeNotFound {
		t.Fatalf("an unknown talkoot = %v", err)
	}
}
