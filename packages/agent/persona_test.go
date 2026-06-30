package agent

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// TestPersonaName_Resolution checks the precedence: TERVA_PERSONA_NAME env >
// persona_name config field > DefaultPersonaName.
func TestPersonaName_Resolution(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("TERVA_PERSONA_NAME", "")

	if got := PersonaName(); got != DefaultPersonaName {
		t.Fatalf("default: got %q, want %q", got, DefaultPersonaName)
	}

	if err := SaveConfig(Config{PersonaName: "Aria"}); err != nil {
		t.Fatal(err)
	}
	if got := PersonaName(); got != "Aria" {
		t.Fatalf("config override: got %q, want %q", got, "Aria")
	}

	t.Setenv("TERVA_PERSONA_NAME", "Sol")
	if got := PersonaName(); got != "Sol" {
		t.Fatalf("env should win over config: got %q, want %q", got, "Sol")
	}
}

// TestBuildSystemPrompt_DefaultPersona: the default identity carries both
// pronunciations, the "mind" meaning, and the mind-in-a-vessel image.
func TestBuildSystemPrompt_DefaultPersona(t *testing.T) {
	got := BuildSystemPrompt(SystemPromptOpts{}) // empty PersonaName -> Mieli
	for _, want := range []string{
		"You are Mieli (pronounced MYEH-lee)",
		"terva (pronounced TEHR-vah)",
		`Finnish for "mind"`,
		"mind in a preserved vessel",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("default identity missing %q\n---\n%s", want, got)
		}
	}
}

// TestBuildSystemPrompt_CustomPersona: a custom name swaps the identity but
// keeps the terva framing and drops the Mieli-specific pronunciation.
func TestBuildSystemPrompt_CustomPersona(t *testing.T) {
	got := BuildSystemPrompt(SystemPromptOpts{PersonaName: "Aria"})
	if !strings.Contains(got, "You are Aria,") {
		t.Errorf("custom identity should name Aria:\n%s", got)
	}
	if strings.Contains(got, "MYEH-lee") {
		t.Errorf("custom identity must not carry the Mieli pronunciation:\n%s", got)
	}
	if !strings.Contains(got, "Finnish for pine tar") {
		t.Errorf("custom identity should keep terva's meaning:\n%s", got)
	}
}

// TestBuildSystemPrompt_CustomReplacesEntirely: an explicit Custom prompt
// still wins over the persona machinery (it replaces the identity wholesale).
func TestBuildSystemPrompt_CustomReplacesEntirely(t *testing.T) {
	got := BuildSystemPrompt(SystemPromptOpts{Custom: "BESPOKE PROMPT", PersonaName: "Aria"})
	if strings.Contains(got, "You are Aria") || strings.Contains(got, "Mieli") {
		t.Errorf("Custom should replace the identity entirely:\n%s", got)
	}
	if !strings.Contains(got, "BESPOKE PROMPT") {
		t.Errorf("Custom text missing:\n%s", got)
	}
}
