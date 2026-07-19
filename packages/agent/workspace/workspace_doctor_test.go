package workspace

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/agent/ctrlproto"
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
