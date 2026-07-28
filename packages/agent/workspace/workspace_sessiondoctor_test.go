package workspace

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/persona"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// The session doctor's persona must resolve by its stem and read like a
// structure-from-play dramaturg, not a card smith or a character editor.
func TestDramaturgPersonaResolves(t *testing.T) {
	p, err := persona.Resolve(dramaturgPersona)
	if err != nil {
		t.Fatalf("resolve %q: %v", dramaturgPersona, err)
	}
	if strings.TrimSpace(p.Charter) == "" {
		t.Fatal("dramaturg persona has an empty charter")
	}
	low := strings.ToLower(p.Charter)
	for _, want := range []string{"scene", "lore", "propose"} {
		if !strings.Contains(low, want) {
			t.Errorf("dramaturg charter missing %q:\n%s", want, p.Charter)
		}
	}
}

// The census flags a recurring mid-sentence proper name that exists nowhere in
// the cast/lore, skips known names and sentence-starts, and surfaces a
// numeric fact stated exactly once (with context) while dropping repeated
// numbers.
func TestDramaturgCensus(t *testing.T) {
	mk := func(text string) provider.Message {
		return provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: text}}}
	}
	msgs := []provider.Message{
		mk("She nodded toward Marrow, the lamplighter. Everyone said Marrow kept the keys."),
		mk("Later, old Marrow waved. The wage was 8s a week, and the ledger showed 41 draughts."),
		mk("The bell rang twice. It rang 41 times in the story she told about the flood."),
		mk("Marrow. That was the whole answer."), // sentence-start — not counted
	}
	known := censusKnownNames("Kobeni", "Kira", []string{"Elira"}, []core.WorldLoreEntry{{Name: "The debt", Audience: []string{"Mirei"}}})
	rep := dramaturgCensus(msgs, known)

	if len(rep.Recurring) != 1 || rep.Recurring[0] != "Marrow" {
		t.Errorf("recurring = %v, want exactly [Marrow] (3 mid-sentence uses)", rep.Recurring)
	}
	// "8s" appears once → flagged with context; "41" appears twice → dropped.
	joined := strings.Join(rep.Singletons, " | ")
	if !strings.Contains(joined, "8s a week") {
		t.Errorf("singleton numeric fact missing its context: %v", rep.Singletons)
	}
	if strings.Contains(joined, "41") {
		t.Errorf("a repeated number is not a singleton: %v", rep.Singletons)
	}
	// Known names never flagged.
	for _, n := range rep.Recurring {
		if n == "Kobeni" || n == "Elira" || n == "Mirei" {
			t.Errorf("census flagged a known name %q", n)
		}
	}
}

// The evidence block carries every section the task contract references:
// the attributed scene, who is on stage, existing lore WITH its scope, and
// the census — plus honest empty states.
func TestRenderDramaturgEvidence(t *testing.T) {
	transcript := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "I open the shop."}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "Kobeni waved."}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "\"Fabric's in.\""}},
			Meta: map[string]string{core.MetaSource: core.MetaDirected, core.MetaActor: "Elira"}},
	}
	lore := []core.WorldLoreEntry{
		{Name: "The bell", Content: "Rings at dusk."},
		{Name: "The debt", Content: "Three favors owed.", Audience: []string{"Mirei"}},
		{Name: core.SceneStateName, Constant: true, Content: "Day 14, first light."},
	}
	// The pin was written at message 0 and three have played: the header must
	// report the drift rather than the bare "(current)" it used to claim.
	out := renderDramaturgEvidence("Kira", "Kobeni", []string{"Elira"}, lore, transcript, len(transcript),
		dramaturgCensusReport{Recurring: []string{"Marrow"}, Singletons: []string{"8s a week"}})
	for _, want := range []string{
		"Kira: I open the shop.",
		"Elira: \"Fabric's in.\"",
		"- Kira (the player)",
		"- Kobeni (the main character)",
		"- Elira (on the roster)",
		"do not re-record",
		"- The bell [shared]: Rings at dusk.",
		"- The debt [known to Mirei]: Three favors owed.",
		"THE PINNED SCENE-STATE CARD",
		"last written 3 message(s) ago",
		"propose a scene_state update",
		"Day 14, first light.",
		"- Marrow",
		"8s a week",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("evidence missing %q:\n%s", want, out)
		}
	}
	// The pin leaves the lore list: "do not re-record" is the wrong rule for a
	// card whose proposals are updates.
	if strings.Contains(out, "- "+core.SceneStateName) {
		t.Errorf("the pin must not appear in the do-not-re-record lore list:\n%s", out)
	}
	empty := renderDramaturgEvidence("Me", "", nil, nil, nil, 0, dramaturgCensusReport{})
	for _, want := range []string{"(the scene has not started yet)", "(nothing pinned yet)", "(nothing recorded yet)", "(nothing flagged)"} {
		if !strings.Contains(empty, want) {
			t.Errorf("empty evidence missing %q:\n%s", want, empty)
		}
	}
}

// Server-side validation: kinds outside the vocabulary are dropped, a
// lore/thread name colliding with existing lore (or an earlier proposal in
// the round) is dropped, a promotion of the bound character or a roster
// member is dropped, cross-kind payload fields are cleared, and ids fill in.
func TestParseSessionDoctorResult(t *testing.T) {
	raw := "prose around it\n```json\n" + `{
	  "note": "three keepers",
	  "proposals": [
	    {"id":"p1","kind":"lore_entry","rationale":"the contract scene","name":"The wage ladder","content":"8s, then 10 after trial.","keys":["wage"],"audience":[]},
	    {"id":"p2","kind":"lore_entry","rationale":"dup","name":"The bell","content":"already recorded"},
	    {"id":"p3","kind":"open_thread","rationale":"promised","name":"The wage ladder","content":"in-round duplicate name"},
	    {"id":"p4","kind":"cast_promotion","rationale":"voiced twice","character":"Marrow","description":"The lamplighter who keeps the keys.","first_mes":"*He lifts the pole.*","name":"should clear"},
	    {"id":"p5","kind":"cast_promotion","rationale":"already bound","character":"Kobeni","description":"x"},
	    {"id":"p6","kind":"cast_promotion","rationale":"already on stage","character":"elira","description":"x"},
	    {"id":"p7","kind":"scene_break","rationale":"the night ends here","name":"The North Road","content":"They part at the door; the next scene opens at first light."},
	    {"id":"p7b","kind":"scene_break","rationale":"a second boundary","name":"Elsewhere","content":"x"},
	    {"kind":"open_thread","rationale":"promised visit","name":"The lieutenant at first light","content":"The watch arrives at dawn.","keys":["lieutenant","dawn"]},
	    {"id":"p8","kind":"lore_entry","rationale":"mis-tagged pin update","name":"scene state","content":"Day 16, dawn. The docks.","keys":["day"],"audience":["Elira"]},
	    {"id":"p9","kind":"scene_state","rationale":"second card in one round","content":"a contradicting card"}
	  ]
	}` + "\n```"
	// The recorded pin proves scene_state is an UPDATE: addressing it is not a
	// collision, unlike p2's re-record of "the bell".
	lore := []core.WorldLoreEntry{{Name: "the bell", Content: "x"}, {Name: core.SceneStateName, Constant: true, Content: "Day 15."}}
	res, err := parseSessionDoctorResult(raw, lore, []string{"Elira"}, "Kobeni")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Note != "three keepers" {
		t.Errorf("note = %q", res.Note)
	}
	if len(res.Proposals) != 5 {
		t.Fatalf("want 5 surviving proposals, got %d: %+v", len(res.Proposals), res.Proposals)
	}
	if res.Proposals[0].Kind != ctrlproto.SessionProposalLore || res.Proposals[0].Name != "The wage ladder" {
		t.Errorf("p1 mangled: %+v", res.Proposals[0])
	}
	promo := res.Proposals[1]
	if promo.Kind != ctrlproto.SessionProposalPromotion || promo.Character != "Marrow" {
		t.Fatalf("promotion mangled: %+v", promo)
	}
	if promo.Name != "" {
		t.Errorf("cross-kind payload not cleared on promotion: %+v", promo)
	}
	// p7 survives as the round's ONE boundary; p7b (a second) is dropped — a
	// scene cannot end in two places.
	brk := res.Proposals[2]
	if brk.Kind != ctrlproto.SessionProposalBreak || brk.Name != "The North Road" {
		t.Fatalf("scene-break proposal mangled: %+v", brk)
	}
	thread := res.Proposals[3]
	if thread.Kind != ctrlproto.SessionProposalThread || thread.ID == "" {
		t.Errorf("thread proposal should survive with a filled id: %+v", thread)
	}
	// p8: a lore_entry addressed to the pin name is RETAGGED scene_state (not
	// collision-dropped), name canonicalized, keys/audience cleared — the pin
	// is always-on and shared by definition. p9, a second card in the same
	// round, is dropped: one card, one proposal.
	state := res.Proposals[4]
	if state.Kind != ctrlproto.SessionProposalState || state.Name != core.SceneStateName || state.Content != "Day 16, dawn. The docks." {
		t.Fatalf("scene-state proposal mangled: %+v", state)
	}
	if len(state.Keys) != 0 || len(state.Audience) != 0 {
		t.Errorf("keys/audience must clear on a scene-state proposal: %+v", state)
	}

	if _, err := parseSessionDoctorResult("no json here", nil, nil, ""); err == nil {
		t.Error("expected an error when the reply carries no JSON object")
	}
}

// lore_retire (SD6) is the only kind whose acceptance DELETES, so its
// validation is the strict one: it may name only an entry that is actually on
// file, never a reserved name, never twice, and never one the same round just
// proposed. The payload is answered from the RECORD, not from the doctor's
// paraphrase — the author is deciding on a deletion and must see what is
// really there.
func TestParseSessionDoctorResult_Retire(t *testing.T) {
	raw := `{"note":"","proposals":[
	  {"id":"r1","kind":"lore_retire","rationale":"Veyra arrived and was asked all of it in the requisition beat","name":"prepare for the first-light search","content":"the doctor's paraphrase","keys":["wrong"]},
	  {"id":"r2","kind":"lore_retire","rationale":"dup","name":"Prepare for the First-Light Search"},
	  {"id":"r3","kind":"lore_retire","rationale":"no such entry","name":"A thing never recorded"},
	  {"id":"r4","kind":"lore_retire","rationale":"the pin has its own verb","name":"Scene state"},
	  {"id":"r5","kind":"lore_retire","rationale":"the recap has its own lifecycle","name":"The story so far"},
	  {"id":"r6","kind":"lore_entry","rationale":"what actually happened","name":"The First-Light Requisition","content":"Veyra signed for 19c 7s.","keys":["requisition"]},
	  {"id":"r7","kind":"lore_retire","rationale":"retiring what this round just proposed","name":"The First-Light Requisition"}
	]}`
	lore := []core.WorldLoreEntry{
		{Name: "Prepare for the First-Light Search", Keys: []string{"rope", "first light"}, Content: "At first light, the watch is expected to organize a search."},
		{Name: core.SceneStateName, Constant: true, Content: "Day 15."},
		{Name: core.StorySoFarName, Constant: true, Content: "Previously…"},
	}
	res, err := parseSessionDoctorResult(raw, lore, nil, "Kobeni")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Proposals) != 2 {
		t.Fatalf("want the one valid retirement + the new entry, got %d: %+v", len(res.Proposals), res.Proposals)
	}
	r := res.Proposals[0]
	if r.Kind != ctrlproto.SessionProposalRetire {
		t.Fatalf("first proposal should be the retirement: %+v", r)
	}
	// Case-insensitive match, answered with the RECORDED spelling so the
	// world.lore.delete the client sends actually hits the entry.
	if r.Name != "Prepare for the First-Light Search" {
		t.Errorf("retire must answer the recorded name, got %q", r.Name)
	}
	// Content and keys come off the record, overwriting the doctor's version.
	if r.Content != "At first light, the watch is expected to organize a search." {
		t.Errorf("retire content must come from the record, got %q", r.Content)
	}
	if len(r.Keys) != 2 || r.Keys[0] != "rope" {
		t.Errorf("retire keys must come from the record, got %v", r.Keys)
	}
	if res.Proposals[1].Kind != ctrlproto.SessionProposalLore {
		t.Errorf("the paired lore_entry should survive: %+v", res.Proposals[1])
	}
}

// The whole verb under a scripted client: an immersive session runs the
// doctor on its OWN client, the proposals come back validated, and the spend
// is booked against the session — the doctor is never free.
func TestSessionsDoctorRunsAndBooks(t *testing.T) {
	reply := `{"note":"ok","proposals":[{"id":"p1","kind":"lore_entry","rationale":"the scene","name":"The bell","content":"Rings at dusk."}]}`
	cl := &scriptedClient{replies: []string{reply}}
	s := worldTestSession(t, cl, map[string]string{"Elira": "elira-ref"})
	var booked []provider.Usage
	s.agent.AddUsageObserver(func(u, _ provider.Usage) { booked = append(booked, u) })

	res, err := sessionsDoctor(context.Background(), s, ctrlproto.SessionDoctorParams{})
	if err != nil {
		t.Fatalf("sessionsDoctor: %v", err)
	}
	if len(res.Proposals) != 1 || res.Proposals[0].Name != "The bell" {
		t.Fatalf("proposals = %+v", res.Proposals)
	}
	if len(booked) != 1 || booked[0] != scriptedCallUsage {
		t.Fatalf("doctor call booked %v, want exactly one %+v — the session doctor is spending unrecorded", booked, scriptedCallUsage)
	}
	// The one request carried the dramaturg's contract and the roster.
	reqs := cl.requests()
	if len(reqs) != 1 {
		t.Fatalf("want exactly one model call, got %d", len(reqs))
	}
	if !strings.Contains(reqs[0].System, "session doctor") {
		t.Errorf("system prompt is not the dramaturg's:\n%.200s", reqs[0].System)
	}
	if !strings.Contains(textOf(reqs[0].Messages[0]), "- Elira (on the roster)") {
		t.Errorf("evidence missing the roster")
	}

	// A coding session is refused before any model call.
	coding := newTurnTestSession(t, cl)
	if _, err := sessionsDoctor(context.Background(), coding, ctrlproto.SessionDoctorParams{}); err == nil {
		t.Error("a coding session should be refused")
	}
}

func textOf(m provider.Message) string {
	var sb strings.Builder
	for _, c := range m.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			sb.WriteString(tb.Text)
		}
	}
	return sb.String()
}

// The small hands (SD2/SD3). A focused run reads the marked moment's
// neighborhood — not the whole session — and never returns a promotion; a
// promote run returns only the named character's promotion and refuses
// anyone already on stage; the two asks are mutually exclusive.
func TestSessionsDoctorFocusAndPromote(t *testing.T) {
	focusReply := `{"proposals":[
	  {"id":"p1","kind":"lore_entry","rationale":"the marked moment","name":"The stew rule","content":"The pot never empties."},
	  {"id":"p2","kind":"cast_promotion","rationale":"drive-by","character":"Marrow","description":"x"},
	  {"id":"p3","kind":"scene_state","rationale":"drive-by rewrite","content":"Day 1, the shop."}]}`
	promoteReply := `{"proposals":[
	  {"id":"p1","kind":"cast_promotion","rationale":"their lines","character":"Marrow","description":"The lamplighter.","first_mes":"*He lifts the pole.*"},
	  {"id":"p2","kind":"lore_entry","rationale":"incidental","name":"Stray","content":"x"}]}`
	cl := &scriptedClient{replies: []string{focusReply, promoteReply}}
	s := worldTestSession(t, cl, map[string]string{"Elira": "elira-ref"})
	// A recognizable early message that must NOT reach a focused run's evidence,
	// then filler, then the marked moment.
	early := provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "UNRELATED-EARLY-BEAT"}}}
	msgs := []provider.Message{early}
	for i := 0; i < focusWindow+2; i++ {
		msgs = append(msgs, provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "filler beat"}}})
	}
	msgs = append(msgs, provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "The pot never empties — house rule."}}})
	s.agent.SetMessages(msgs)
	focus := len(msgs) - 1

	res, err := sessionsDoctor(context.Background(), s, ctrlproto.SessionDoctorParams{Focus: &focus})
	if err != nil {
		t.Fatalf("focused run: %v", err)
	}
	if len(res.Proposals) != 1 || res.Proposals[0].Kind != ctrlproto.SessionProposalLore {
		t.Fatalf("a focused run keeps only lore/thread proposals (no promotions, no scene-state rewrites), got %+v", res.Proposals)
	}
	evidence := textOf(cl.requests()[0].Messages[0])
	if !strings.Contains(evidence, "THE MARKED MOMENT") || !strings.Contains(evidence, "The pot never empties") {
		t.Errorf("focused evidence missing the marked moment")
	}
	if strings.Contains(evidence, "UNRELATED-EARLY-BEAT") {
		t.Errorf("focused evidence leaked beyond the neighborhood window")
	}

	res, err = sessionsDoctor(context.Background(), s, ctrlproto.SessionDoctorParams{Promote: "Marrow"})
	if err != nil {
		t.Fatalf("promote run: %v", err)
	}
	if len(res.Proposals) != 1 || res.Proposals[0].Character != "Marrow" {
		t.Fatalf("a promote run keeps only the named promotion, got %+v", res.Proposals)
	}
	if !strings.Contains(textOf(cl.requests()[1].Messages[0]), "exactly ONE cast_promotion proposal for Marrow") {
		t.Errorf("promote evidence missing the narrowed ask")
	}

	// Refusals, all before any model call: on-stage promote, bad index, both asks.
	if _, err := sessionsDoctor(context.Background(), s, ctrlproto.SessionDoctorParams{Promote: "Elira"}); err == nil {
		t.Error("promoting a roster member should be refused")
	}
	bad := len(msgs)
	if _, err := sessionsDoctor(context.Background(), s, ctrlproto.SessionDoctorParams{Focus: &bad}); err == nil {
		t.Error("an out-of-range focus should be refused")
	}
	zero := 0
	if _, err := sessionsDoctor(context.Background(), s, ctrlproto.SessionDoctorParams{Focus: &zero, Promote: "Marrow"}); err == nil {
		t.Error("focus+promote together should be refused")
	}
	if got := len(cl.requests()); got != 2 {
		t.Errorf("refusals must spend nothing: %d model calls, want 2", got)
	}
}
