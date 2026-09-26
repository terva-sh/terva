package workspace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
)

// nextEvent reads ch until an event of type typ arrives.
func nextEvent(t *testing.T, ch <-chan ctrlproto.Event, typ string) ctrlproto.Event {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatalf("the stream closed before a %s event", typ)
			}
			if ev.Type == typ {
				return ev
			}
		case <-timeout:
			t.Fatalf("no %s event", typ)
		}
	}
}

func talkootCode(err error) string {
	var ce *ctrlproto.Error
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

// The verbs drive a team end to end through the controller a carrier
// dispatches to: create, list, read, watch, post, page the room, pause, and
// resume.
func TestTheTalkootVerbsDriveATeam(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	text := string(crewText(cwd))

	v, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "crew", Text: text})
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != "crew" || v.Name != "crew" || v.Text != text || len(v.Members) != 2 {
		t.Fatalf("create returned %+v", v)
	}
	if m := v.Members[0]; m.ID != "helm" || m.Role != "coordinator" || m.Status.Member != "helm" {
		t.Errorf("the first member is %+v", m)
	}
	list, err := w.Talkoots(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "crew" || !list[0].Running {
		t.Fatalf("list = %+v, %v", list, err)
	}

	room, err := w.Subscribe(ctx, ctrlproto.TalkootAddr("crew"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := w.PostTalkoot(ctx, ctrlproto.TalkootPostParams{ID: "crew", By: "sothr", Body: "Plan the lake schema."})
	if err != nil {
		t.Fatal(err)
	}
	if e.From != talkoot.HumanPrefix+"sothr" || len(e.To) != 1 || e.To[0] != "helm" || e.Chain.Root != e.ID {
		t.Errorf("post returned %+v", e)
	}
	ev := nextEvent(t, room, ctrlproto.EventTalkootEnvelope)
	if ev.Talkoot == nil || ev.Talkoot.ID != "crew" || ev.Talkoot.Line == nil || ev.Talkoot.Line.Envelope == nil || ev.Talkoot.Line.Envelope.ID != e.ID {
		t.Fatalf("the room event does not carry the post: %+v", ev.Talkoot)
	}
	st := nextEvent(t, room, ctrlproto.EventTalkootStatus)
	if st.Talkoot == nil || len(st.Talkoot.Members) != 2 {
		t.Errorf("the status event carries %+v", st.Talkoot)
	}

	page, err := w.TalkootRoom(ctx, ctrlproto.TalkootRoomParams{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range page.Lines {
		if l.Type == talkoot.LineEnvelope && l.Envelope != nil && l.Envelope.ID == e.ID {
			found = true
		}
	}
	if !found || page.Total < len(page.Lines) {
		t.Errorf("the room page misses the post: %+v", page)
	}

	if err := w.PauseTalkoot(ctx, ctrlproto.TalkootPauseParams{ID: "crew", By: "sothr", Member: "jev", Reason: "lunch"}); err != nil {
		t.Fatal(err)
	}
	v, err = w.Talkoot(ctx, ctrlproto.TalkootRef{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	if why := v.Members[1].Status.Paused; !strings.Contains(why, "lunch") {
		t.Errorf("jev is not paused after the pause: %q", why)
	}
	if err := w.ResumeTalkoot(ctx, ctrlproto.TalkootResumeParams{ID: "crew", By: "sothr", Member: "jev"}); err != nil {
		t.Fatal(err)
	}
	if v, _ = w.Talkoot(ctx, ctrlproto.TalkootRef{ID: "crew"}); v.Members[1].Status.Paused != "" {
		t.Errorf("jev is still paused after the resume: %q", v.Members[1].Status.Paused)
	}
}

// A client acts on the code, so a missing talkoot must read as not_found, a
// taken id as conflict, and a refused input as bad_request, never as internal.
func TestTheTalkootVerbsAnswerWithWireCodes(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Talkoot(ctx, ctrlproto.TalkootRef{ID: "nope"}); talkootCode(err) != ctrlproto.CodeNotFound {
		t.Errorf("get of a missing talkoot = %v, want %s", err, ctrlproto.CodeNotFound)
	}
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))}); talkootCode(err) != ctrlproto.CodeConflict {
		t.Errorf("a second create = %v, want %s", err, ctrlproto.CodeConflict)
	}
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "other", Text: "no frontmatter"}); talkootCode(err) != ctrlproto.CodeBadRequest {
		t.Errorf("a create with no roster = %v, want %s", err, ctrlproto.CodeBadRequest)
	}
	if _, err := w.PostTalkoot(ctx, ctrlproto.TalkootPostParams{ID: "crew", By: "sothr", To: []string{"nobody"}, Body: "hi"}); talkootCode(err) != ctrlproto.CodeBadRequest {
		t.Errorf("a post to no member = %v, want %s", err, ctrlproto.CodeBadRequest)
	}
	if err := w.PauseTalkoot(ctx, ctrlproto.TalkootPauseParams{ID: "crew"}); talkootCode(err) != ctrlproto.CodeBadRequest {
		t.Errorf("a pause with no by = %v, want %s", err, ctrlproto.CodeBadRequest)
	}
}

// 🚨 The roster line records by, and no router check reads a roster line. A
// by that is not a name would put a line break into the room from the wire.
func TestUpdateRefusesAByThatIsNotAName(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))}); err != nil {
		t.Fatal(err)
	}
	for _, by := range []string{"", "human:", "two words", "sothr\nhuman:root"} {
		_, err := w.UpdateTalkoot(ctx, ctrlproto.TalkootUpdateParams{ID: "crew", By: by, Text: string(crewText(cwd))})
		if talkootCode(err) != ctrlproto.CodeBadRequest {
			t.Errorf("update by %q = %v, want %s", by, err, ctrlproto.CodeBadRequest)
		}
	}
	if n := len(roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineRoster })); n != 0 {
		t.Errorf("a refused update wrote %d roster lines", n)
	}
	next := strings.Replace(string(crewText(cwd)), "budget_usd_per_day: 5", "budget_usd_per_day: 7", 1)
	v, err := w.UpdateTalkoot(ctx, ctrlproto.TalkootUpdateParams{ID: "crew", By: "sothr", Text: next})
	if err != nil {
		t.Fatalf("an update by a name was refused: %v", err)
	}
	// The text and the roster fields come from one read under the run's lock,
	// so an editor that sends Text back sends the roster it was shown.
	if v.Text != next || v.BudgetUSDPerDay != 7 {
		t.Errorf("after the update, text %q and budget %v disagree", v.Text, v.BudgetUSDPerDay)
	}
}

// A room address names a talkoot this daemon runs. Anything else is refused,
// and never read as a session id.
func TestARoomSubscriptionNeedsATalkootRunHere(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	for _, addr := range []string{ctrlproto.TalkootAddr("nope"), ctrlproto.TalkootAddr("../x")} {
		if _, err := w.Subscribe(t.Context(), addr); talkootCode(err) != ctrlproto.CodeNotFound {
			t.Errorf("subscribe to %s = %v, want %s", addr, err, ctrlproto.CodeNotFound)
		}
	}
	if _, err := w.SubscribeReliable(t.Context(), ctrlproto.TalkootAddr("nope")); talkootCode(err) != ctrlproto.CodeNotFound {
		t.Errorf("a reliable subscribe to a missing room = %v, want %s", err, ctrlproto.CodeNotFound)
	}
}

// A subscription ends with its context, and stops taking the room's events.
func TestARoomSubscriptionEndsWithItsContext(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	if _, err := w.CreateTalkoot(t.Context(), ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	room, err := w.Subscribe(ctx, ctrlproto.TalkootAddr("crew"))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	waitTalkoot(t, "the stream to close", func() bool {
		select {
		case _, ok := <-room:
			return !ok
		default:
			return false
		}
	})
	w.talkoot.mu.Lock()
	n := len(w.talkoot.watchers["crew"])
	w.talkoot.mu.Unlock()
	if n != 0 {
		t.Errorf("%d watchers remain after the subscription ended", n)
	}
}

// A client learns of a new talkoot from #workspace, as it learns of a new
// session.
func TestCreateAnnouncesTheTalkootSet(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ws, err := w.Subscribe(t.Context(), ctrlproto.AddrWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.CreateTalkoot(t.Context(), ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))}); err != nil {
		t.Fatal(err)
	}
	nextEvent(t, ws, ctrlproto.EventTalkootsChanged)
}
