package workspace

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/provider"
)

func suggestMsg(role provider.Role, text string) provider.Message {
	return provider.Message{Role: role, Content: []provider.Content{provider.TextBlock{Text: text}}, Time: time.Time{}}
}

// Worlds W1: voicing a library card writes the actor task for the card's name
// and grounds the draft in the card's authored fields, superseding a typed voice.
func TestRenderSuggestSystem_ActorFromCard(t *testing.T) {
	c := &card.Card{
		Name:        "Elira",
		Description: "a mistress of the high tower",
		Personality: "imperious, precise",
		Scenario:    "the tower at dusk",
	}
	// A card is passed AND a stray typed voice — the card must win.
	sys := renderSuggestSystem(suggestTarget{kind: "actor", voice: "ignored walk-on", card: c}, userPersona{Name: "Aria"}, nil, "", nil)
	if !strings.Contains(sys, "next line for Elira") {
		t.Errorf("actor task not written for the card's name:\n%s", sys)
	}
	for _, want := range []string{"a mistress of the high tower", "imperious, precise", "the tower at dusk"} {
		if !strings.Contains(sys, want) {
			t.Errorf("card voice missing %q:\n%s", want, sys)
		}
	}
	if strings.Contains(sys, "ignored walk-on") {
		t.Errorf("typed voice should be superseded by the card:\n%s", sys)
	}
}

func TestRenderSuggestSystem_IncludesPersonaCardAndScene(t *testing.T) {
	c := &card.Card{
		Name:        "Kobeni",
		Description: "a nervous convenience-store clerk",
		Personality: "anxious, kind",
		Scenario:    "the late shift",
	}
	transcript := []provider.Message{
		suggestMsg(provider.RoleAssistant, "*She looks up from the register.* Oh — welcome!"),
		suggestMsg(provider.RoleUser, "I grab a coffee."),
	}
	sys := renderSuggestSystem(suggestTarget{}, userPersona{Name: "Aki", Description: "a tired regular"}, c, "", transcript)

	for _, want := range []string{
		"first person",         // the drafting instruction
		"NOT the character",    // the do-not-speak-as-them guardrail
		"Aki: a tired regular", // the player persona
		"name: Kobeni",         // the character block
		"a nervous convenience-store clerk",
		"the late shift",
		"Kobeni: *She looks up", // character-labelled transcript line
		"Aki: I grab a coffee.", // player-labelled transcript line
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt missing %q\n---\n%s", want, sys)
		}
	}
}

func TestRenderSuggestSystem_NoPersonaNoCard(t *testing.T) {
	transcript := []provider.Message{suggestMsg(provider.RoleAssistant, "Hello there.")}
	sys := renderSuggestSystem(suggestTarget{}, userPersona{}, nil, "", transcript)

	if !strings.Contains(sys, "not specified") {
		t.Errorf("expected an unspecified-player note, got:\n%s", sys)
	}
	if strings.Contains(sys, "THE CHARACTER THE PLAYER IS TALKING TO") {
		t.Errorf("no card → no character block, got:\n%s", sys)
	}
	// The unnamed player's transcript lines fall back to "Me:".
	if !strings.Contains(sys, "the character: Hello there.") {
		t.Errorf("expected a fallback character label, got:\n%s", sys)
	}
}

func TestRenderSuggestSystem_EmptyTranscript(t *testing.T) {
	sys := renderSuggestSystem(suggestTarget{}, userPersona{Name: "Aki"}, nil, "", nil)
	if !strings.Contains(sys, "the scene has not started yet") {
		t.Errorf("expected an empty-scene note, got:\n%s", sys)
	}
}

func TestRenderTranscriptTail_BudgetKeepsMostRecent(t *testing.T) {
	msgs := []provider.Message{
		suggestMsg(provider.RoleUser, "OLDEST oldest oldest"),
		suggestMsg(provider.RoleAssistant, "middle middle"),
		suggestMsg(provider.RoleUser, "NEWEST"),
	}
	// A budget that fits only the last line or two.
	tail := renderTranscriptTail(msgs, "Me", "Bot", 20)
	if !strings.Contains(tail, "NEWEST") {
		t.Errorf("tail must keep the most-recent line, got: %q", tail)
	}
	if strings.Contains(tail, "OLDEST") {
		t.Errorf("tail should have dropped the oldest line under budget, got: %q", tail)
	}
}

func TestRenderTranscriptTail_SkipsNonProse(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.ToolCallBlock{Name: "actor_spawn"}}},
		suggestMsg(provider.RoleUser, "hi"),
	}
	tail := renderTranscriptTail(msgs, "Me", "Bot", 8000)
	if strings.Contains(tail, "actor_spawn") {
		t.Errorf("tool blocks must be skipped, got: %q", tail)
	}
	if !strings.Contains(tail, "Me: hi") {
		t.Errorf("prose line must survive, got: %q", tail)
	}
}

func TestSuggestGuidance_EmptyBecomesInstruction(t *testing.T) {
	if got := suggestGuidance("  "); !strings.Contains(got, "no specific guidance") {
		t.Errorf("blank guidance should become an explicit instruction, got: %q", got)
	}
	if got := suggestGuidance("be brief"); got != "be brief" {
		t.Errorf("non-empty guidance should pass through, got: %q", got)
	}
}
