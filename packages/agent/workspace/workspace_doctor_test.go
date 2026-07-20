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
	out := renderDoctorPrompt(fields, findings, decisions)

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

func TestRenderDoctorPromptNoDecisionsOmitsSection(t *testing.T) {
	out := renderDoctorPrompt(doctorFields(card.Card{Name: "Ivy"}), nil, nil)
	if strings.Contains(out, "DECISIONS") {
		t.Errorf("first-pass prompt should not carry a decisions section:\n%s", out)
	}
	if !strings.Contains(out, "(none)") {
		t.Errorf("no findings should render as (none):\n%s", out)
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
