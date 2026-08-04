package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/raati"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
	"terva.sh/terva/packages/vote"
)

// freshHome points TERVA_HOME at a throwaway dir so record I/O and
// persona resolution stay hermetic (the build package has its own copy;
// this is the package-workspace twin).
func freshHome(t *testing.T) string {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("TERVA_PERSONA_NAME", "")
	return home
}

// TestFoldRaatiEvent replays a full deliberation's event feed onto the
// board view and checks every state the client renders from.
func TestFoldRaatiEvent(t *testing.T) {
	v := &ctrlproto.RaatiView{Running: true, Question: "q", Class: "advisory"}
	accents := map[string]string{"YATA-1": "#d8dee9", "KUSANAGI-2": "#8fa3bf", "MAGATAMA-3": "#73daca"}
	fold := func(ev raati.Event) { foldRaatiEvent(v, accents, ev) }

	for _, name := range []string{"YATA-1", "KUSANAGI-2", "MAGATAMA-3"} {
		fold(raati.Event{Kind: raati.EventSeated, Unit: name, AgentID: "a-" + name})
	}
	if len(v.Units) != 3 || v.Units[0].Accent != "#d8dee9" || v.Units[0].Status != "deliberating" {
		t.Fatalf("after seating: %+v", v.Units)
	}

	fold(raati.Event{Kind: raati.EventRound, Round: 1})
	fold(raati.Event{Kind: raati.EventVoted, Unit: "YATA-1", Round: 1,
		Ballot: &vote.Ballot{Unit: "YATA-1", Verdict: vote.Approve, Confidence: 0.8, Rationale: "holds"}})
	fold(raati.Event{Kind: raati.EventVoted, Unit: "KUSANAGI-2", Round: 1,
		Ballot: &vote.Ballot{Unit: "KUSANAGI-2", Verdict: vote.Reject, Confidence: 0.7, Rationale: "risky"}})
	fold(raati.Event{Kind: raati.EventAbsent, Unit: "MAGATAMA-3", Round: 1, Why: "timed out"})

	if v.Units[0].Status != "voted" || v.Units[0].Blind != "approve" || v.Units[0].Rationale != "holds" {
		t.Errorf("YATA-1 after blind vote: %+v", v.Units[0])
	}
	if v.Units[2].Status != "absent" || v.Units[2].Why != "timed out" {
		t.Errorf("MAGATAMA-3 after absence: %+v", v.Units[2])
	}

	// Cross-examination reopens the floor for voted units only; the
	// provisional verdict stays visible on the block.
	fold(raati.Event{Kind: raati.EventRound, Round: 2})
	if v.Round != 2 {
		t.Errorf("Round = %d, want 2", v.Round)
	}
	if v.Units[0].Status != "deliberating" || v.Units[0].Verdict != "approve" {
		t.Errorf("YATA-1 in cross-exam: %+v", v.Units[0])
	}
	if v.Units[2].Status != "absent" {
		t.Errorf("absent unit must stay absent in round 2: %+v", v.Units[2])
	}

	// Final votes: KUSANAGI-2 is persuaded and revises.
	fold(raati.Event{Kind: raati.EventVoted, Unit: "YATA-1", Round: 2,
		Ballot: &vote.Ballot{Unit: "YATA-1", Verdict: vote.Approve, Confidence: 0.85, Rationale: "still holds"}})
	fold(raati.Event{Kind: raati.EventVoted, Unit: "KUSANAGI-2", Round: 2,
		Ballot: &vote.Ballot{Unit: "KUSANAGI-2", Verdict: vote.Approve, Confidence: 0.6, Rationale: "persuaded"}})
	if v.Units[1].Verdict != "approve" || v.Units[1].Blind != "reject" {
		t.Errorf("revision must keep the blind verdict visible: %+v", v.Units[1])
	}

	// A per-turn reseat re-emits seated for the same unit: the block
	// upserts (new binding, back to deliberating, held verdict kept)
	// rather than duplicating.
	fold(raati.Event{Kind: raati.EventSeated, Unit: "YATA-1", AgentID: "a-YATA-1-r2", Binding: "openrouter/z.ai/glm-5.2"})
	if len(v.Units) != 3 {
		t.Fatalf("reseat duplicated the unit: %d blocks", len(v.Units))
	}
	if v.Units[0].Binding != "openrouter/z.ai/glm-5.2" || v.Units[0].Status != "deliberating" || v.Units[0].Verdict != "approve" {
		t.Errorf("reseated block = %+v, want new binding, deliberating, held verdict", v.Units[0])
	}
	fold(raati.Event{Kind: raati.EventVoted, Unit: "YATA-1", Round: 2,
		Ballot: &vote.Ballot{Unit: "YATA-1", Verdict: vote.Approve, Confidence: 0.85, Rationale: "still holds"}})

	outcome := vote.Tally([]vote.Ballot{
		{Unit: "YATA-1", Verdict: vote.Approve, Confidence: 0.85},
		{Unit: "KUSANAGI-2", Verdict: vote.Approve, Confidence: 0.6},
		vote.AbsentBallot("MAGATAMA-3", "timed out"),
	}, vote.Majority{})
	fold(raati.Event{Kind: raati.EventDecided, Outcome: &outcome})

	if v.Running || v.Decision != "approved" || !v.Degraded {
		t.Errorf("after decision: running=%v decision=%q degraded=%v", v.Running, v.Decision, v.Degraded)
	}
	if v.Tally == nil || v.Tally.Approve != 2 || v.Tally.Absent != 1 {
		t.Errorf("tally = %+v", v.Tally)
	}
}

func msgText(role provider.Role, text string) provider.Message {
	return provider.Message{Role: role, Content: []provider.Content{provider.TextBlock{Text: text}}}
}

func TestRenderConversationTail(t *testing.T) {
	msgs := []provider.Message{
		msgText(provider.RoleUser, "first question"),
		msgText(provider.RoleAssistant, "first answer"),
		{Role: provider.RoleAssistant}, // no text blocks: skipped
		msgText(provider.RoleUser, "second question"),
		msgText(provider.RoleAssistant, "second answer"),
	}
	full, truncated := renderConversationTail(msgs, 1<<20)
	if truncated {
		t.Errorf("truncated on a roomy limit")
	}
	want := "user: first question\nassistant: first answer\nuser: second question\nassistant: second answer"
	if full != want {
		t.Errorf("full render = %q", full)
	}
	// A tight limit keeps the NEWEST lines and reports the drop.
	tail, truncated := renderConversationTail(msgs, len("user: second question\nassistant: second answer")+2)
	if !truncated || !strings.HasPrefix(tail, "user: second question") || !strings.Contains(tail, "second answer") {
		t.Errorf("tail render = %q (truncated=%v), want the newest exchange", tail, truncated)
	}
	if strings.Contains(tail, "first") {
		t.Errorf("tail render kept old material past the limit: %q", tail)
	}
	if out, _ := renderConversationTail(nil, 100); out != "" {
		t.Errorf("empty transcript rendered %q", out)
	}
}

func TestAssembleRaatiEvidence(t *testing.T) {
	w := &Workspace{diag: func(string) {}}

	if got := w.assembleRaatiEvidence("q", raatiEvidenceSpec{}); got != "" {
		t.Errorf("empty spec produced evidence: %q", got)
	}

	got := w.assembleRaatiEvidence("q", raatiEvidenceSpec{user: "  the CI logs say X  "})
	if !strings.Contains(got, "evidence submitted with the question") || !strings.Contains(got, "the CI logs say X") {
		t.Errorf("user evidence missing: %q", got)
	}

	got = w.assembleRaatiEvidence("q", raatiEvidenceSpec{
		conversation: "full",
		messages:     []provider.Message{msgText(provider.RoleUser, "we discussed Y")},
	})
	if !strings.Contains(got, "conversation this question arose from") || !strings.Contains(got, "user: we discussed Y") {
		t.Errorf("full conversation attachment missing: %q", got)
	}

	// Oversized pasted evidence is cut WITH disclosure.
	got = w.assembleRaatiEvidence("q", raatiEvidenceSpec{user: strings.Repeat("a", raatiUserEvidenceCap+10)})
	if !strings.Contains(got, "stops at 32KiB") {
		t.Errorf("oversized evidence not disclosed: %d bytes", len(got))
	}

	// Summary mode runs the (stubbed — the real one spends a model
	// turn) summarizer and labels its brief.
	w.raati.summarize = func(q, conv string) (string, error) {
		if !strings.Contains(conv, "we discussed Z") {
			t.Errorf("summarizer input missing the conversation: %q", conv)
		}
		return "brief: Z was discussed", nil
	}
	got = w.assembleRaatiEvidence("q", raatiEvidenceSpec{
		conversation: "summary",
		messages:     []provider.Message{msgText(provider.RoleUser, "we discussed Z")},
	})
	if !strings.Contains(got, "summarized for this panel") || !strings.Contains(got, "brief: Z was discussed") {
		t.Errorf("summary attachment missing: %q", got)
	}

	// A failed summary discloses the gap instead of failing the
	// deliberation.
	w.raati.summarize = func(q, conv string) (string, error) { return "", context.DeadlineExceeded }
	got = w.assembleRaatiEvidence("q", raatiEvidenceSpec{
		conversation: "summary",
		messages:     []provider.Message{msgText(provider.RoleUser, "we discussed Z")},
	})
	if !strings.Contains(got, "the summary failed") {
		t.Errorf("summary failure not disclosed: %q", got)
	}
}

func TestRaatiSeatOverrides(t *testing.T) {
	if pool, err := raatiSeatOverrides(map[string]string{}, 3); err != nil || pool != nil {
		t.Errorf("no seats chosen: pool=%v err=%v, want nil/nil (config fallback)", pool, err)
	}
	full := map[string]string{
		"provider_0": "openai-codex", "model_0": "gpt-5.5",
		"provider_1": "opencode-go", "model_1": "qwen3.7-plus",
		"provider_2": "openrouter", "model_2": "z.ai/glm-5.2",
	}
	pool, err := raatiSeatOverrides(full, 3)
	if err != nil || len(pool) != 3 || pool[2].Model != "z.ai/glm-5.2" {
		t.Errorf("full panel: pool=%v err=%v", pool, err)
	}
	if _, err := raatiSeatOverrides(map[string]string{"provider_1": "x", "model_1": "y"}, 3); err == nil {
		t.Errorf("partial panel must be rejected")
	}
	if _, err := raatiSeatOverrides(map[string]string{"provider_0": "x"}, 3); err == nil {
		t.Errorf("half a seat must be rejected")
	}
}

func TestRaatiRecordNanos(t *testing.T) {
	if n, ok := raatiRecordNanos("raati-1783569369589109000.json"); !ok || n != 1783569369589109000 {
		t.Errorf("valid id rejected: %d %v", n, ok)
	}
	for _, id := range []string{
		"", "raati-.json", "raati-abc.json", "notraati-1.json",
		"raati-1.json.bak", "../raati-1.json", "raati-1/../../etc.json",
		"raati--5.json", "raati-0.json",
	} {
		if _, ok := raatiRecordNanos(id); ok {
			t.Errorf("bad id %q accepted", id)
		}
	}
}

func TestRaatiHistoryAndViewFromRecord(t *testing.T) {
	freshHome(t)
	older := &raati.Result{
		Question: "older question",
		Class:    raati.ClassAdvisory,
		Units: []raati.UnitRecord{
			{Name: "YATA-1", Persona: "raati-crew:yata", Provider: "ollama", Model: "qwen3:8b"},
		},
		Blind: []vote.Ballot{{Unit: "YATA-1", Verdict: vote.Reject, Confidence: 0.6, Rationale: "doubts"}},
		Final: []vote.Ballot{{Unit: "YATA-1", Verdict: vote.Approve, Confidence: 0.8, Rationale: "persuaded"}},
		Outcome: vote.Tally([]vote.Ballot{
			{Unit: "YATA-1", Verdict: vote.Approve},
			{Unit: "MAGATAMA-3", Verdict: vote.Approve},
			{Unit: "KUSANAGI-2", Verdict: vote.Reject, Rationale: "risk"},
		}, vote.Majority{Quorum: 1}),
	}
	newer := &raati.Result{
		Question: "newer question",
		Class:    raati.ClassGate,
		Final:    []vote.Ballot{vote.AbsentBallot("YATA-1", "timed out")},
		Units:    []raati.UnitRecord{{Name: "YATA-1", Persona: "raati-crew:yata"}},
		Outcome:  vote.Tally([]vote.Ballot{vote.AbsentBallot("YATA-1", "timed out")}, vote.Unanimity{}),
	}
	p1, err := build.WriteRaatiRecord(older)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := build.WriteRaatiRecord(newer); err != nil {
		t.Fatal(err)
	}

	hist := raatiHistory(10)
	if len(hist) != 2 {
		t.Fatalf("history = %d entries, want 2", len(hist))
	}
	if hist[0].Question != "newer question" || hist[1].Question != "older question" {
		t.Errorf("history order wrong: %q then %q", hist[0].Question, hist[1].Question)
	}
	if hist[0].Decision != "escalated" || !hist[0].Degraded || hist[1].Decision != "approved" {
		t.Errorf("history summaries wrong: %+v", hist)
	}
	if hist[0].When == "" || hist[0].ID == "" {
		t.Errorf("history entry missing id/when: %+v", hist[0])
	}
	if hist[1].Tally == nil || hist[1].Tally.Approve != 2 || hist[1].Tally.Reject != 1 {
		t.Errorf("history tally = %+v, want 2·1 from the older record", hist[1].Tally)
	}
	if len(hist[1].Minority) != 1 || hist[1].Minority[0] != "KUSANAGI-2" {
		t.Errorf("history minority = %v, want the dissenting unit's name", hist[1].Minority)
	}

	// The archived view rebuilt from the older record.
	nanos, _ := raatiRecordNanos(filepath.Base(p1))
	v := raatiViewFromRecord(older, nanos)
	if !v.Archived || v.Running || v.When == "" {
		t.Errorf("archived view flags: %+v", v)
	}
	u := v.Units[0]
	if u.Status != "voted" || u.Verdict != "approve" || u.Blind != "reject" || u.Binding != "ollama/qwen3:8b" {
		t.Errorf("archived unit = %+v, want the revision visible with its binding", u)
	}
	if v.Decision != "approved" || v.Tally == nil || v.Tally.Approve != 2 {
		t.Errorf("archived outcome = %+v", v)
	}

	// raatiShow rejects traversal-shaped ids and loads real ones.
	w := &Workspace{sessions: map[string]*wsSession{}}
	if err := w.raatiShow("../" + filepath.Base(p1)); err == nil {
		t.Errorf("traversal id accepted")
	}
	if err := w.raatiShow(filepath.Base(p1)); err != nil {
		t.Fatalf("raatiShow: %v", err)
	}
	got := w.raatiView()
	if !got.Archived || got.Question != "older question" {
		t.Errorf("board after show = %+v", got)
	}
	if len(got.History) != 2 {
		t.Errorf("board history = %d, want 2", len(got.History))
	}
}

// TestFoldRaatiEventTolerantOfUnknowns: events for units the view
// doesn't know (or with missing payloads) must not panic or corrupt
// the board — the feed and the view can skew across restarts.
func TestFoldRaatiEventTolerantOfUnknowns(t *testing.T) {
	v := &ctrlproto.RaatiView{}
	foldRaatiEvent(v, nil, raati.Event{Kind: raati.EventVoted, Unit: "GHOST-9", Round: 1,
		Ballot: &vote.Ballot{Verdict: vote.Approve}})
	foldRaatiEvent(v, nil, raati.Event{Kind: raati.EventVoted, Unit: "GHOST-9", Round: 1})
	foldRaatiEvent(v, nil, raati.Event{Kind: raati.EventAbsent, Unit: "GHOST-9"})
	foldRaatiEvent(v, nil, raati.Event{Kind: raati.EventDecided})
	if len(v.Units) != 0 || v.Decision != "" {
		t.Errorf("unknown-unit events corrupted the view: %+v", v)
	}
}

func TestParseClerkAnswers(t *testing.T) {
	good := "prose\n```answers\n[\"two days\", \"NOT_IN_RECORD\"]\n```\n"
	if a, ok := parseClerkAnswers(good, 2); !ok || a[0] != "two days" {
		t.Fatalf("parse = %v, %v", a, ok)
	}
	for name, bad := range map[string]string{
		"no fence":     `["a"]`,
		"bad json":     "```answers\n[oops]\n```",
		"wrong length": "```answers\n[\"only one\"]\n```",
	} {
		if _, ok := parseClerkAnswers(bad, 2); ok {
			t.Errorf("%s: parsed, want failure", name)
		}
	}
}

func TestFoldRaatiInquiryEvent(t *testing.T) {
	v := &ctrlproto.RaatiView{}
	foldRaatiEvent(v, nil, raati.Event{Kind: raati.EventInquiry, Unit: "YATA-1", Round: 1,
		Why: "is there budget?", Answer: "no", Source: raati.SourceRecord})
	foldRaatiEvent(v, nil, raati.Event{Kind: raati.EventInquiry, Unit: "MAGATAMA-3", Round: 1,
		Why: "who maintains it?", Source: raati.SourceUnanswered})
	if len(v.Inquiries) != 2 {
		t.Fatalf("Inquiries = %+v, want 2", v.Inquiries)
	}
	if q := v.Inquiries[0]; q.Unit != "YATA-1" || q.Question != "is there budget?" || q.Answer != "no" || q.Source != "record" {
		t.Errorf("inquiry 0 = %+v", q)
	}
	if v.Inquiries[1].Source != "unanswered" {
		t.Errorf("inquiry 1 = %+v", v.Inquiries[1])
	}
}

// TestRaatiAskConvener: rung 2 — questions the record couldn't answer
// surface as ONE ask on the convening session carrying the whole open
// docket; answers land with source convener, declines stay open, and
// everything already answered is never re-asked.
func TestRaatiAskConvener(t *testing.T) {
	w := &Workspace{}
	var asked []string
	sets := 0
	w.raati.ask = func(_ context.Context, _ *wsSession, set []core.UserQuestion) ([]core.UserAnswer, error) {
		sets++
		out := make([]core.UserAnswer, len(set))
		for i, q := range set {
			asked = append(asked, q.Question)
			if strings.Contains(q.Question, "budget") {
				out[i] = core.UserAnswer{Answer: "yes — Q3 has headroom"}
				continue
			}
			out[i] = core.UserAnswer{Declined: true}
		}
		return out, nil
	}
	qs := []raati.Inquiry{
		{Unit: "YATA-1", Question: "is there budget?", Source: raati.SourceUnanswered, Round: 1},
		{Unit: "KUSANAGI-2", Question: "already answered", Source: raati.SourceRecord, Answer: "yes", Round: 1},
		{Unit: "MAGATAMA-3", Question: "who maintains it?", Source: raati.SourceUnanswered, Round: 1},
	}
	out := w.raatiAskConvener(context.Background(), &wsSession{}, "ship it?", qs)
	if len(asked) != 2 {
		t.Fatalf("asked = %v, want the two open questions only", asked)
	}
	if sets != 1 {
		t.Fatalf("the docket went over in %d asks, want 1 — the convener should see it whole", sets)
	}
	if out[0].Source != raati.SourceConvener || out[0].Answer != "yes — Q3 has headroom" {
		t.Errorf("answered inquiry = %+v", out[0])
	}
	if out[1].Source != raati.SourceRecord || out[1].Answer != "yes" {
		t.Errorf("record inquiry disturbed: %+v", out[1])
	}
	if out[2].Source != raati.SourceUnanswered || out[2].Answer != "" {
		t.Errorf("declined inquiry = %+v, want open", out[2])
	}
}

// TestRaatiAskConvenerDeadline: an expired budget leaves the rest of
// the docket open instead of stalling the deliberation.
func TestRaatiAskConvenerDeadline(t *testing.T) {
	w := &Workspace{}
	w.raati.ask = func(ctx context.Context, _ *wsSession, set []core.UserQuestion) ([]core.UserAnswer, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	qs := []raati.Inquiry{
		{Unit: "YATA-1", Question: "one?", Source: raati.SourceUnanswered},
		{Unit: "MAGATAMA-3", Question: "two?", Source: raati.SourceUnanswered},
	}
	out := w.raatiAskConvener(ctx, &wsSession{}, "q", qs)
	for i, q := range out {
		if q.Source != raati.SourceUnanswered {
			t.Errorf("inquiry %d = %+v, want open after deadline", i, q)
		}
	}
}

// TestRaatiProfileArgDefaults: the board half of "explicit call beats
// profile beats config" — a named profile fills only the form fields
// the client omitted.
func TestRaatiProfileArgDefaults(t *testing.T) {
	boolp := func(v bool) *bool { return &v }
	intp := func(v int) *int { return &v }
	prof := raati.Profile{
		Class: "gate", Level: intp(1), SingleRound: boolp(true),
		Inquire: "record", Converge: boolp(true), SeatOrder: "fixed",
	}

	args := map[string]string{"question": "q"}
	raatiProfileArgDefaults(args, prof)
	for k, want := range map[string]string{
		"class": "gate", "single_round": "true",
		"inquire": "record", "rounds": "3", "seat_order": "fixed",
	} {
		if args[k] != want {
			t.Errorf("filled args[%q] = %q, want %q", k, args[k], want)
		}
	}
	// Level never rides the args map — raatiConvene resolves it via
	// PickLevel (auto needs the config picture, which strings can't
	// carry).
	if _, ok := args["level"]; ok {
		t.Errorf("level leaked into the args map: %q", args["level"])
	}

	// Everything the form said explicitly survives — including the
	// explicit "off" that exists precisely to override the profile.
	args = map[string]string{
		"question": "q", "class": "veto", "level": "0",
		"inquire": "off", "rounds": "2", "seat_order": "turn",
	}
	raatiProfileArgDefaults(args, prof)
	for k, want := range map[string]string{
		"class": "veto", "level": "0", "inquire": "off",
		"rounds": "2", "seat_order": "turn",
	} {
		if args[k] != want {
			t.Errorf("overridden args[%q] = %q, want %q", k, args[k], want)
		}
	}

	// Seats never ride the args map either — they replace the level2
	// pool in raatiConvene directly, and their implied level 2 comes
	// out of PickLevel there.
	args = map[string]string{"question": "q"}
	raatiProfileArgDefaults(args, raati.Profile{Seats: []raati.Binding{
		{Provider: "a", Model: "1"}, {Provider: "b", Model: "2"}, {Provider: "c", Model: "3"},
	}})
	if _, ok := args["provider_0"]; ok {
		t.Error("profile seats leaked into the args map")
	}
	if _, ok := args["level"]; ok {
		t.Error("seats-implied level leaked into the args map")
	}
}
