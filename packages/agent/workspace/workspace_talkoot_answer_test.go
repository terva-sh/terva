package workspace

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
)

// A person's answer to a seated member's question becomes a sealed room line.
// The member's tool result names the answer: reference, and a send that cites
// it carries the person's answer to the recipient.
func TestAPersonsAnswerIsRecordedAndCitable(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	inner := toolCallProvider("ask_user_question", `{"question":"Accept the cut?","options":["Accept cut","Keep it"]}`)
	provider := func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		inner(w, r)
	}
	w, room := startInboxCrew(t, provider, "auto-edit")
	ctx := t.Context()

	card := *nextEvent(t, room, ctrlproto.EventTalkootInbox).Talkoot.Card
	if err := w.Answer(ctx, card.Session, card.ID, []core.UserAnswer{{Answer: "Accept cut", Note: "after the demo"}}); err != nil {
		t.Fatal(err)
	}
	line := nextEvent(t, room, ctrlproto.EventTalkootAnswer).Talkoot.Line
	if line.Type != talkoot.LineAnswer || line.Member != "helm" || line.Ref == "" || len(line.Answers) != 1 {
		t.Fatalf("the room event carried %+v, want helm's answer line", line)
	}
	if a := line.Answers[0]; a.Question != "Accept the cut?" || len(a.Chosen) != 1 || a.Chosen[0] != "Accept cut" || a.Note != "after the demo" {
		t.Fatalf("answer line holds %+v", a)
	}
	cite := "answer:" + line.Ref
	if !regexp.MustCompile(`^answer:[0-9A-Z]{26}$`).MatchString(cite) {
		t.Fatalf("the reference %q is not an answer: reference a send accepts", cite)
	}
	waitTalkoot(t, "helm's turn to end", func() bool { return !memberView(t, w, "crew", "helm").Status.Working })

	// The tool result that the model read names the reference.
	mu.Lock()
	var result string
	for _, b := range bodies {
		if strings.Contains(b, `"role":"tool"`) {
			result = b
		}
	}
	mu.Unlock()
	if !strings.Contains(result, "records this answer as "+cite) {
		t.Fatalf("helm's tool result does not name %s: %s", cite, result)
	}

	seat, ok := w.talkootSeatOf(card.Session)
	if !ok {
		t.Fatal("helm holds no seat")
	}
	e, err := seat.Send(talkoot.Outgoing{To: []string{"jev"}, Kind: talkoot.KindNote, Body: "sothr accepted the cut.", Refs: []string{cite}})
	if err != nil {
		t.Fatalf("a send that cites the answer: %v", err)
	}
	if len(e.Cites) != 1 || e.Cites[0].Answer != line.Ref || e.Cites[0].Asker != "helm" {
		t.Fatalf("the envelope cites %+v", e.Cites)
	}
	if wire := wireTalkootEnvelope(e); len(wire.Cites) != 1 || wire.Cites[0].At.IsZero() || wire.Cites[0].Answers[0].Chosen[0] != "Accept cut" {
		t.Fatalf("the wire envelope cites %+v", wire.Cites)
	}
	if _, err := seat.Send(talkoot.Outgoing{To: []string{"jev"}, Kind: talkoot.KindNote, Body: "sothr said ship it.", Refs: []string{"answer:01K0000000000000000000000A"}}); err == nil {
		t.Error("a send that cites no answer in the room must be refused")
	}

	// A plain Ask, as ticket_init and the raati clerk make, records nothing:
	// its caller cannot hand a reference on. The cited ask above is the
	// positive control, and it wrote one line.
	helm := w.existing(card.Session)
	if n := roomAnswers(t, w); n != 1 {
		t.Fatalf("the room holds %d answer lines after the cited ask, want 1", n)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = (&webAsker{s: helm}).Ask(ctx, []core.UserQuestion{{Question: "Who records this store?"}})
	}()
	plain := *nextEvent(t, room, ctrlproto.EventTalkootInbox).Talkoot.Card
	if err := w.Answer(ctx, plain.Session, plain.ID, []core.UserAnswer{{Answer: "sothr"}}); err != nil {
		t.Fatal(err)
	}
	<-done
	if n := roomAnswers(t, w); n != 1 {
		t.Errorf("a plain Ask wrote an answer line: the room holds %d, want 1", n)
	}

	// A short answer set records a decline for each question it leaves out,
	// and never indexes past the end.
	short := helm.recordTalkootAnswer([]core.UserQuestion{{Question: "Ship it?"}, {Question: "Today?"}}, []core.UserAnswer{{Answer: "Ship it"}})
	if short.Ref == "" || short.Err != nil {
		t.Fatalf("a short answer set recorded %+v", short)
	}
	page, err := w.talkootRoom(ctx, "crew", 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	// A card line from the question before can land after the answer line,
	// through the run's queue, so the test reads the last answer line.
	var last talkoot.Line
	for _, l := range page.Lines {
		if l.Type == talkoot.LineAnswer {
			last = l
		}
	}
	if last.Ref != short.Ref[len("answer:"):] || len(last.Answers) != 2 || last.Answers[0].Declined || !last.Answers[1].Declined {
		t.Errorf("the short answer line: %+v", last)
	}

	// A seat that cannot record returns the error, so the member learns that
	// no teammate can check the answer.
	seat.b.mu.Lock()
	seat.b.revoked = true
	seat.b.mu.Unlock()
	if rec := helm.recordTalkootAnswer([]core.UserQuestion{{Question: "Q?"}}, []core.UserAnswer{{Answer: "A"}}); rec.Ref != "" || rec.Err == nil {
		t.Errorf("a revoked seat recorded %+v, want an error", rec)
	}
}

// A session with no seat asks as before: no room line and no reference.
func TestAnUnseatedAnswerRecordsNothing(t *testing.T) {
	s := newTestSession()
	s.id = "loner"
	if got := s.recordTalkootAnswer([]core.UserQuestion{{Question: "Q?"}}, []core.UserAnswer{{Answer: "A"}}); got != (tools.AnswerRecord{}) {
		t.Fatalf("a session outside any workspace returned %+v", got)
	}
	cwd := talkootHome(t)
	s.ws = openTalkootWorkspace(t, cwd)
	if got := s.recordTalkootAnswer([]core.UserQuestion{{Question: "Q?"}}, []core.UserAnswer{{Answer: "A"}}); got != (tools.AnswerRecord{}) {
		t.Fatalf("a session with no seat returned %+v", got)
	}
}

func roomAnswers(t *testing.T, w *Workspace) int {
	t.Helper()
	page, err := w.talkootRoom(t.Context(), "crew", 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range page.Lines {
		if l.Type == talkoot.LineAnswer {
			n++
		}
	}
	return n
}
