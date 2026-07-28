package build

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/persona"
)

const immersivePersonaRaw = `---
name: Data
pronunciation: DAY-tuh
specialty: starship operations officer
immersive: true
good_for: [starship-operations]
---

You are Lieutenant Commander Data, operations and science officer. You command
a starship through its systems, exposed to you as tools.`

func TestPersonaImmersiveParse(t *testing.T) {
	p, err := persona.Parse(immersivePersonaRaw, "test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !p.Immersive {
		t.Fatalf("expected Immersive=true")
	}
	if !strings.Contains(p.Charter, "Lieutenant Commander Data") {
		t.Fatalf("charter not captured: %q", p.Charter)
	}
}

func TestImmersiveCustomPrecedence(t *testing.T) {
	imm, _ := persona.Parse(immersivePersonaRaw, "test")
	additive := imm
	additive.Immersive = false

	cases := []struct {
		name   string
		custom string
		p      persona.Persona
		want   string // "" means keep additive path
	}{
		{"immersive, no custom -> charter wins", "", imm, imm.Charter},
		{"explicit custom always wins", "SYSTEM.md body", imm, ""},
		{"additive persona -> no replace", "", additive, ""},
		{"immersive but empty charter -> no replace", "", persona.Persona{Immersive: true}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := persona.ImmersiveCustom(c.custom, c.p); got != c.want {
				t.Fatalf("persona.ImmersiveCustom(%q,...) = %q, want %q", c.custom, got, c.want)
			}
		})
	}
}

// TestImmersiveReplacesIdentity is the observable end-to-end check: the same
// charter produces a coding-assistant-framed prompt when additive, and a
// charter-only prompt when immersive.
func TestImmersiveReplacesIdentity(t *testing.T) {
	p, _ := persona.Parse(immersivePersonaRaw, "test")

	// Additive path: Custom empty, name+charter layered.
	additive := BuildSystemPrompt(SystemPromptOpts{
		PersonaName: p.Name,
		Charter:     p.Charter,
	})
	if !strings.Contains(additive, "expert coding assistant") {
		t.Errorf("additive prompt should retain the harness coding identity")
	}
	if !strings.Contains(additive, "Lieutenant Commander Data") {
		t.Errorf("additive prompt should include the charter")
	}

	// Immersive path: routing sets Custom = charter.
	custom := persona.ImmersiveCustom("", p)
	immersive := BuildSystemPrompt(SystemPromptOpts{
		Custom:      custom,
		PersonaName: p.Name,
		Charter:     p.Charter,
	})
	if strings.Contains(immersive, "expert coding assistant") {
		t.Errorf("immersive prompt should NOT contain the coding-assistant identity")
	}
	if !strings.Contains(immersive, "Lieutenant Commander Data") {
		t.Errorf("immersive prompt should be the charter")
	}
}
