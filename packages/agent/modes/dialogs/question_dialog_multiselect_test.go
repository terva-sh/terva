package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

// The terminal half of multi-select. The selection set is the whole change:
// a cursor answers "where am I looking", which on a single-select list happens
// to also answer "what did I choose". On a list you can give several answers to,
// those come apart — and a cursor can hold neither "these three" nor "none of
// them" distinctly from "I haven't moved yet".

func multiDialog(t *testing.T, qs ...core.UserQuestion) (*QuestionDialog, *QuestionRequest) {
	t.Helper()
	d := NewQuestionDialog()
	req := &QuestionRequest{Questions: qs, Resp: make(chan []core.UserAnswer, 1)}
	d.Enqueue(req)
	return d, req
}

func multiQuestion() core.UserQuestion {
	return core.UserQuestion{
		Question:    "Which to enable?",
		Options:     []string{"redis", "postgres", "s3"},
		MultiSelect: true,
	}
}

func space() tui.Key { return tui.Key{Kind: tui.KeyRune, Rune: ' '} }
func down() tui.Key  { return tui.Key{Kind: tui.KeyDown} }
func enter() tui.Key { return tui.Key{Kind: tui.KeyEnter} }

func typeRunes(d *QuestionDialog, s string) {
	for _, r := range s {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
}

func TestMultiSelectTicksAccumulate(t *testing.T) {
	d, req := multiDialog(t, multiQuestion())

	d.HandleKey(space()) // redis
	d.HandleKey(down())
	d.HandleKey(down())
	d.HandleKey(space()) // s3
	d.HandleKey(enter())

	ans := <-req.Resp
	if got := strings.Join(ans[0].Answers, "|"); got != "redis|s3" {
		t.Fatalf("Answers = %q; want redis|s3 — ticks must accumulate, not replace", got)
	}
	// The joined mirror travels too, for every reader that predates the list.
	if ans[0].Answer != "redis, s3" {
		t.Errorf("Answer mirror = %q; want %q", ans[0].Answer, "redis, s3")
	}
	if ans[0].Declined {
		t.Error("a submitted selection must not read as declined")
	}
}

func TestMultiSelectSpaceUnticks(t *testing.T) {
	d, req := multiDialog(t, multiQuestion())

	d.HandleKey(space())
	d.HandleKey(space()) // same row again
	d.HandleKey(down())
	d.HandleKey(space()) // postgres
	d.HandleKey(enter())

	ans := <-req.Resp
	if got := strings.Join(ans[0].Answers, "|"); got != "postgres" {
		t.Fatalf("Answers = %q; want postgres — a second space on a row unticks it", got)
	}
}

// Nothing ticked is an ANSWER — "none of these" — and it is not the same fact
// as declining. Only esc declines. If this collapsed into Declined the model
// would be told to proceed on its best judgment when the user had in fact just
// told it, precisely, to do none of them.
func TestMultiSelectEmptyIsAnAnswerNotADecline(t *testing.T) {
	d, req := multiDialog(t, multiQuestion())

	d.HandleKey(enter())

	ans := <-req.Resp
	if ans[0].Declined {
		t.Fatal("an empty selection was reported as declined; 'none of these' is an answer")
	}
	if len(ans[0].Answers) != 0 {
		t.Fatalf("Answers = %q; want empty", ans[0].Answers)
	}
	if ans[0].Answers == nil {
		t.Error("Answers is nil, so the daemon cannot tell an empty multi-select from a single-select answer")
	}
}

// The bug this guards is the one that would have been most expensive: reaching
// the custom row and pressing enter used to resolve the answer as the typed
// text ALONE, throwing away every tick — at the moment of commit, with no way
// to notice.
func TestMultiSelectCustomTextJoinsTheTicksRatherThanReplacingThem(t *testing.T) {
	q := multiQuestion()
	q.AllowCustom = true
	d, req := multiDialog(t, q)

	d.HandleKey(space()) // redis
	// Down to the custom row (3 options, so it is row index 3).
	d.HandleKey(down())
	d.HandleKey(down())
	d.HandleKey(down())
	d.HandleKey(enter()) // opens the editor
	typeRunes(d, "  vector db  ")
	d.HandleKey(enter()) // commits, and sends

	ans := <-req.Resp
	if got := strings.Join(ans[0].Answers, "|"); got != "redis|vector db" {
		t.Fatalf("Answers = %q; want redis|vector db — typed text is one value among the ticks", got)
	}
}

// A multi-select note is about the whole answer: there is no single option for
// it to be about. Written against one row and then left behind by the cursor,
// it must still be sent — the single-select rule (a note applies only while its
// own option is selected) would silently drop it here.
func TestMultiSelectNoteIsScopedToTheQuestion(t *testing.T) {
	d, req := multiDialog(t, multiQuestion())

	d.HandleKey(space())                               // tick redis
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'}) // note, from row 0
	typeRunes(d, "only if the migration lands")
	d.HandleKey(tui.Key{Kind: tui.KeyEsc}) // back to the list, note kept
	d.HandleKey(down())
	d.HandleKey(space()) // tick postgres, cursor now row 1
	d.HandleKey(enter())

	ans := <-req.Resp
	if ans[0].Note != "only if the migration lands" {
		t.Fatalf("Note = %q; want it to survive the cursor moving off the row it was typed on", ans[0].Note)
	}
	if got := strings.Join(ans[0].Answers, "|"); got != "redis|postgres" {
		t.Errorf("Answers = %q; want redis|postgres", got)
	}
}

// Single-select is untouched. Space cannot mean "tick" on a list where every
// row excludes every other, and an answer from one must carry no list — a
// daemon reading Answers would otherwise see a set where one thing was chosen.
func TestSingleSelectIsUnaffected(t *testing.T) {
	d, req := multiDialog(t, core.UserQuestion{
		Question: "Which one?",
		Options:  []string{"redis", "postgres"},
	})

	d.HandleKey(space()) // must do nothing
	d.HandleKey(down())
	d.HandleKey(enter())

	ans := <-req.Resp
	if ans[0].Answer != "postgres" {
		t.Fatalf("Answer = %q; want postgres — enter still sends the highlighted option", ans[0].Answer)
	}
	if ans[0].Answers != nil {
		t.Errorf("Answers = %q; a single-select answer must carry no list", ans[0].Answers)
	}
}

// The list has to SAY it is additive. Without the boxes a highlighted row and a
// chosen row look identical, and nothing on screen tells the user what enter is
// about to send.
func TestMultiSelectRendersTickBoxesAndACount(t *testing.T) {
	d, _ := multiDialog(t, multiQuestion())
	th := tui.Theme{Muted: 8, Tool: 2, Accent: 4, SelectionFG: 7, SelectionBG: 4}

	plain := func() string {
		var sb strings.Builder
		for _, r := range d.Render(th, 72) {
			sb.WriteString(widgets.StripANSIBytes(r))
			sb.WriteString("\n")
		}
		return sb.String()
	}

	before := plain()
	if !strings.Contains(before, "[ ] redis") {
		t.Errorf("no empty tick box on an unselected option:\n%s", before)
	}
	if !strings.Contains(before, "0 selected") {
		t.Errorf("the count is not shown, so an empty submit is invisible:\n%s", before)
	}
	// The single-select wording would be a lie about a list you can give
	// several answers to.
	if strings.Contains(before, "choose an answer") {
		t.Errorf("multi-select is labelled as a single choice:\n%s", before)
	}

	d.HandleKey(space())
	after := plain()
	if !strings.Contains(after, "[x] redis") {
		t.Errorf("a ticked option is not marked:\n%s", after)
	}
	if !strings.Contains(after, "1 selected") {
		t.Errorf("the count did not follow the tick:\n%s", after)
	}
	if !strings.Contains(after, "space ticks") {
		t.Errorf("the key that does the thing the question asks for is unadvertised:\n%s", after)
	}
}

// The review tab is where a set is checked before it is sent, so the one row a
// user would most want to catch — "I am about to answer none" — must not render
// as a blank line, which reads as a rendering fault rather than a decision.
func TestReviewTabNamesAnEmptyMultiSelect(t *testing.T) {
	d, _ := multiDialog(t,
		core.UserQuestion{Question: "Which database?", Options: []string{"Postgres", "SQLite"}},
		multiQuestion(),
	)
	th := tui.Theme{Muted: 8, Tool: 2, Accent: 4, SelectionFG: 7, SelectionBG: 4}

	d.HandleKey(tui.Key{Kind: tui.KeyTab}) // question 2
	d.HandleKey(tui.Key{Kind: tui.KeyTab}) // review

	var sb strings.Builder
	for _, r := range d.Render(th, 72) {
		sb.WriteString(widgets.StripANSIBytes(r))
		sb.WriteString("\n")
	}
	if !strings.Contains(sb.String(), "none of the options") {
		t.Errorf("the review tab does not say what an empty multi-select will send:\n%s", sb.String())
	}
}
