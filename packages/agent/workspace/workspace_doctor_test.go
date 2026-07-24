package workspace

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// The card doctor's system prompt is the Seppä persona's charter; it must resolve
// by its ASCII stem (the constant the doctor uses).
func TestDoctorPersonaResolves(t *testing.T) {
	p, err := build.ResolvePersona(doctorPersona)
	if err != nil {
		t.Fatalf("resolve doctor persona %q: %v", doctorPersona, err)
	}
	if strings.TrimSpace(p.Charter) == "" {
		t.Fatal("doctor persona has an empty charter")
	}
	if !strings.Contains(strings.ToLower(p.Charter), "card") {
		t.Errorf("doctor charter does not read like a card-craft persona:\n%s", p.Charter)
	}
}

func TestRenderDoctorPromptIncludesFieldsFindingsDecisions(t *testing.T) {
	fields := doctorFields(card.Card{
		Name:        "Ivy",
		Description: "A florist.",
		FirstMes:    "Hey {{user)}.",
	})
	findings := []card.Finding{
		{Rule: "malformed-macro", Severity: card.SevWarn, Field: "first_mes", Message: "Malformed macro", Detail: "{{user)}"},
	}
	decisions := []ctrlproto.DoctorDecision{
		{ProposalID: "p1", Field: "description", Accepted: false, Reason: "keep the backstory"},
	}
	out := renderDoctorPrompt(fields, findings, decisions, "")

	for _, want := range []string{"Ivy", "A florist.", "Hey {{user)}.", "first_mes", "Malformed macro", "{{user)}", "DECLINED", "keep the backstory"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing %q:\n%s", want, out)
		}
	}
	// An empty field is labeled, not silently dropped.
	if !strings.Contains(out, "personality: (empty)") {
		t.Errorf("prompt should mark empty fields:\n%s", out)
	}
}

// The editor (W4) is the doctor's session mode: its persona must resolve and
// read like a promotion-from-play editor, not a card-lint smith.
func TestEditorPersonaResolves(t *testing.T) {
	p, err := build.ResolvePersona(editorPersona)
	if err != nil {
		t.Fatalf("resolve editor persona %q: %v", editorPersona, err)
	}
	if strings.TrimSpace(p.Charter) == "" {
		t.Fatal("editor persona has an empty charter")
	}
	low := strings.ToLower(p.Charter)
	if !strings.Contains(low, "scene") || !strings.Contains(low, "promot") {
		t.Errorf("editor charter does not read like promotion-from-play:\n%s", p.Charter)
	}
}

// The editor's evidence block: the scene is speaker-attributed (a routed line
// carries its actor, a directed narrator beat reads Narrator) and the lore is
// the pre-filtered set the character is cleared for.
func TestRenderEditorEvidence(t *testing.T) {
	transcript := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "Who has the ledger?"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "\"Not me,\" Ivy lies."}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "\"I do.\""}},
			Meta: map[string]string{core.MetaSource: core.MetaRouted, core.MetaActor: "Elira"}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "Rain hammers the roof."}},
			Meta: map[string]string{core.MetaSource: core.MetaDirected}},
	}
	lore := []core.WorldLoreEntry{{Name: "the-debt", Content: "Elira owes the guild.", Audience: []string{"Elira"}}}
	out := renderEditorEvidence("Elira", "Ivy", "Kira", lore, transcript)
	for _, want := range []string{
		"THE PLAYED SCENE",
		"Kira: Who has the ledger?",
		"Ivy: \"Not me,\" Ivy lies.", // the bound character label is the char name passed... (see below)
		"Elira: \"I do.\"",
		"Narrator: Rain hammers the roof.",
		"WHAT ELIRA KNOWS OF THIS WORLD",
		"the-debt: Elira owes the guild.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("evidence missing %q:\n%s", want, out)
		}
	}
	if got := renderEditorEvidence("Elira", "Ivy", "Me", nil, nil); !strings.Contains(got, "(nothing recorded)") || !strings.Contains(got, "scene has not started") {
		t.Errorf("empty evidence should say so:\n%s", got)
	}
}

// The editor mode gates on a live immersive session — a coding session (or an
// unknown one) is refused before any model resolution.
func TestEditorModeGates(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Elira","first_mes":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	coding, err := w.CreateSession(ctx, ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.CardsDoctor(ctx, ctrlproto.DoctorParams{ID: imported.ID, Session: coding.ID})
	if err == nil || !strings.Contains(err.Error(), "chat or play") {
		t.Errorf("a coding session should be refused, got %v", err)
	}
	if _, err := w.CardsDoctor(ctx, ctrlproto.DoctorParams{ID: imported.ID, Session: "no-such-session"}); err == nil {
		t.Error("an unknown session should be refused")
	}
}

// EDITOR mode spends on the SESSION's own model — the one the author is playing
// that scene on — not the workspace default. This is the fix for the enrich
// surface that silently ran on the workspace model regardless of the session
// agent; reverting the resolution to w.args makes the one request carry the
// wrong (here, empty) model and this fails.
func TestEditorModeRunsOnSessionModel(t *testing.T) {
	cl := &scriptedClient{replies: []string{`{"note":"ok","proposals":[]}`}}
	s := worldTestSession(t, cl, map[string]string{"Elira": "elira-ref"})
	s.provider, s.model = "test", "session-model"

	if _, err := cardsDoctor(context.Background(), s.ws, s, card.Card{Name: "Elira"}, ctrlproto.DoctorParams{}); err != nil {
		t.Fatalf("cardsDoctor: %v", err)
	}
	reqs := cl.requests()
	if len(reqs) != 1 {
		t.Fatalf("want exactly one model call, got %d", len(reqs))
	}
	if reqs[0].Model != "session-model" {
		t.Errorf("editor ran on %q, want the session's model %q — enrich is ignoring the session agent", reqs[0].Model, "session-model")
	}
}

func TestRenderDoctorPromptNoDecisionsOmitsSection(t *testing.T) {
	out := renderDoctorPrompt(doctorFields(card.Card{Name: "Ivy"}), nil, nil, "")
	if strings.Contains(out, "DECISIONS") {
		t.Errorf("first-pass prompt should not carry a decisions section:\n%s", out)
	}
	if !strings.Contains(out, "(none)") {
		t.Errorf("no findings should render as (none):\n%s", out)
	}
	if strings.Contains(out, "WHAT THE AUTHOR ASKED FOR") {
		t.Errorf("an unsteered pass should not claim the author asked for anything:\n%s", out)
	}
}

// The steer is the author's standing instruction for the pass. It must reach the
// prompt as its own labeled section (a bare paste among the card fields would
// read as card content), and it goes LAST — after the decisions — because it
// directs the whole round rather than answering one proposal.
func TestRenderDoctorPromptCarriesTheAuthorSteer(t *testing.T) {
	decisions := []ctrlproto.DoctorDecision{{ProposalID: "p1", Field: "description", Accepted: false, Reason: "keep the backstory"}}
	out := renderDoctorPrompt(doctorFields(card.Card{Name: "Ivy"}), nil, decisions, "  make her wearier, and cut the war years  ")

	if !strings.Contains(out, "WHAT THE AUTHOR ASKED FOR") {
		t.Errorf("steer section missing:\n%s", out)
	}
	if !strings.Contains(out, "make her wearier, and cut the war years") {
		t.Errorf("the steer text itself is missing:\n%s", out)
	}
	// The doctor's default posture is lint-first; a steer has to outrank it or
	// the author's instruction becomes a suggestion the model may skip.
	if !strings.Contains(out, "primary warrant") {
		t.Errorf("the steer is not framed as taking precedence:\n%s", out)
	}
	if strings.Index(out, "WHAT THE AUTHOR ASKED FOR") < strings.Index(out, "keep the backstory") {
		t.Errorf("the steer should come after the per-proposal decisions:\n%s", out)
	}
}

// Whitespace is not an instruction: a steer box the author tabbed through must
// not add a section telling the doctor to prioritize nothing.
func TestRenderDoctorPromptIgnoresABlankSteer(t *testing.T) {
	out := renderDoctorPrompt(doctorFields(card.Card{Name: "Ivy"}), nil, nil, "   \n\t ")
	if strings.Contains(out, "WHAT THE AUTHOR ASKED FOR") {
		t.Errorf("a blank steer should add no section:\n%s", out)
	}
}

// End to end through the verb: a steer accepted by the params and dropped on the
// floor before the request is the shape of bug this whole PR exists to add, so
// pin it at the wire rather than at the renderer.
func TestSteerReachesTheModelRequest(t *testing.T) {
	cl := &scriptedClient{replies: []string{`{"note":"ok","proposals":[]}`}}
	s := worldTestSession(t, cl, map[string]string{"Elira": "elira-ref"})
	s.provider, s.model = "test", "session-model"

	p := ctrlproto.DoctorParams{Steer: "she should not know about the Accord yet"}
	if _, err := cardsDoctor(context.Background(), s.ws, s, card.Card{Name: "Elira"}, p); err != nil {
		t.Fatalf("cardsDoctor: %v", err)
	}
	reqs := cl.requests()
	if len(reqs) != 1 {
		t.Fatalf("want exactly one model call, got %d", len(reqs))
	}
	var user string
	for _, c := range reqs[0].Messages[0].Content {
		if tb, ok := c.(provider.TextBlock); ok {
			user += tb.Text
		}
	}
	if !strings.Contains(user, p.Steer) {
		t.Errorf("the author's steer never reached the request:\n%s", user)
	}
}

func TestParseDoctorResult(t *testing.T) {
	fields := doctorFields(card.Card{
		Name:        "Ivy",
		Description: "A florist with a sharp tongue.",
		FirstMes:    "Hey {{user)}, welcome in.",
	})
	// A reply wrapped in prose + code fences, with one real fix, one no-op, one
	// unknown field, and one empty after.
	raw := "Here are my suggestions:\n```json\n" + `{
  "note": "One macro fix.",
  "proposals": [
    {"id":"p1","field":"first_mes","severity":"warn","rationale":"fix macro","after":"Hey {{user}}, welcome in."},
    {"id":"p2","field":"description","severity":"suggestion","rationale":"noop","after":"A florist with a sharp tongue."},
    {"id":"p3","field":"nonsense_field","severity":"info","rationale":"bad","after":"x"},
    {"id":"p4","field":"personality","severity":"info","rationale":"empty","after":""}
  ]
}` + "\n```\n"
	res, err := parseDoctorResult(raw, fields)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Note != "One macro fix." {
		t.Errorf("note = %q", res.Note)
	}
	if len(res.Proposals) != 1 {
		t.Fatalf("want 1 surviving proposal (fix only), got %d: %+v", len(res.Proposals), res.Proposals)
	}
	p := res.Proposals[0]
	if p.Field != "first_mes" || p.After != "Hey {{user}}, welcome in." {
		t.Errorf("proposal = %+v", p)
	}
	// Before is the card's real current value, not whatever the model echoed.
	if p.Before != "Hey {{user)}, welcome in." {
		t.Errorf("before should be the authoritative current value, got %q", p.Before)
	}
}

// A removal is the third thing a doctor must be able to propose, next to new
// text and changed text — "cut the war backstory" had no representation at all,
// because an empty `after` is dropped. `remove` says it explicitly.
func TestParseDoctorResultRemoval(t *testing.T) {
	fields := doctorFields(card.Card{
		Name:         "Ivy",
		Description:  "A florist.",
		SystemPrompt: "Answer in verse.",
		// Personality is empty — nothing there to clear.
	})
	raw := `{"proposals":[
	  {"id":"r1","field":"system_prompt","severity":"suggestion","rationale":"the card does not need an override","after":"","remove":true},
	  {"id":"r2","field":"description","severity":"info","rationale":"echoed the old value anyway","after":"A florist.","remove":true},
	  {"id":"r3","field":"personality","severity":"info","rationale":"already empty","after":"","remove":true}
	]}`
	res, err := parseDoctorResult(raw, fields)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	byField := map[string]ctrlproto.DoctorProposal{}
	for _, p := range res.Proposals {
		byField[p.Field] = p
	}
	sp, ok := byField["system_prompt"]
	if !ok {
		t.Fatalf("the removal was dropped: %+v", res.Proposals)
	}
	if !sp.Remove || sp.After != "" {
		t.Errorf("a removal must survive as remove=true with an empty after, got %+v", sp)
	}
	if sp.Before != "Answer in verse." {
		t.Errorf("before should still be the authoritative current value, got %q", sp.Before)
	}
	// A model that says "remove" and then echoes the old text is proposing a
	// deletion, not a no-op: the app applies `after`, so the echo must not win.
	d, ok := byField["description"]
	if !ok {
		t.Fatalf("a removal whose after echoed the old value was dropped: %+v", res.Proposals)
	}
	if d.After != "" {
		t.Errorf("remove must clear the echoed after, got %q", d.After)
	}
	// Clearing an already-empty field changes nothing — the same no-op rule the
	// ordinary path applies.
	if _, ok := byField["personality"]; ok {
		t.Errorf("clearing an empty field is a no-op and should be dropped: %+v", res.Proposals)
	}
}

// ...and the flag is what makes it a removal: an empty `after` on its own is
// still indistinguishable from a model that had nothing to say, so it stays
// dropped. (TestParseDoctorResult's p4 covers the same rule from the other side.)
func TestParseDoctorResultStillDropsAnUnmarkedEmptyAfter(t *testing.T) {
	fields := doctorFields(card.Card{Name: "Ivy", SystemPrompt: "Answer in verse."})
	res, err := parseDoctorResult(`{"proposals":[{"id":"x","field":"system_prompt","after":""}]}`, fields)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Proposals) != 0 {
		t.Errorf("an empty after without remove should be dropped, got %+v", res.Proposals)
	}
}

func TestParseDoctorResultRejectsGarbage(t *testing.T) {
	if _, err := parseDoctorResult("I could not help with that.", doctorFields(card.Card{})); err == nil {
		t.Error("expected an error when the reply carries no JSON object")
	}
}

func TestParseDoctorResultOrdersWarningsFirst(t *testing.T) {
	fields := doctorFields(card.Card{Description: "d", Personality: "p", Scenario: "s"})
	raw := `{"proposals":[
	  {"id":"a","field":"scenario","severity":"suggestion","after":"s2"},
	  {"id":"b","field":"description","severity":"warn","after":"d2"},
	  {"id":"c","field":"personality","severity":"info","after":"p2"}
	]}`
	res, err := parseDoctorResult(raw, fields)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Proposals) != 3 || res.Proposals[0].Severity != "warn" {
		t.Fatalf("warning should sort first: %+v", res.Proposals)
	}
}
