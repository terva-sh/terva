package build

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

// TestPersonaName_Resolution checks the precedence: TERVA_PERSONA_NAME env >
// persona_name config field > DefaultPersonaName.
func TestPersonaName_Resolution(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("TERVA_PERSONA_NAME", "")

	if got := config.PersonaName(); got != config.DefaultPersonaName {
		t.Fatalf("default: got %q, want %q", got, config.DefaultPersonaName)
	}

	if err := config.SaveConfig(config.Config{PersonaName: "Aria"}); err != nil {
		t.Fatal(err)
	}
	if got := config.PersonaName(); got != "Aria" {
		t.Fatalf("config override: got %q, want %q", got, "Aria")
	}

	t.Setenv("TERVA_PERSONA_NAME", "Sol")
	if got := config.PersonaName(); got != "Sol" {
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
// still wins over the Persona machinery (it replaces the identity wholesale).
func TestBuildSystemPrompt_CustomReplacesEntirely(t *testing.T) {
	got := BuildSystemPrompt(SystemPromptOpts{Custom: "BESPOKE PROMPT", PersonaName: "Aria"})
	if strings.Contains(got, "You are Aria") || strings.Contains(got, "Mieli") {
		t.Errorf("Custom should replace the identity entirely:\n%s", got)
	}
	if !strings.Contains(got, "BESPOKE PROMPT") {
		t.Errorf("Custom text missing:\n%s", got)
	}
}

// TestBuildSystemPrompt_IntroOverride: an intro override (a native Persona's
// agent_introduction, or a card's system_prompt) replaces the branded intro but
// KEEPS terva's conventions bracketing — the additive-with-custom-intro middle
// ground — and carries its provenance label.
func TestBuildSystemPrompt_IntroOverride(t *testing.T) {
	opts := SystemPromptOpts{
		PersonaName:   "Aria",
		IntroOverride: "I am Aria of the deep.",
		IntroSource:   "persona:introduction",
	}
	got := BuildSystemPrompt(opts)
	if !strings.Contains(got, "I am Aria of the deep.") {
		t.Errorf("intro override text missing:\n%s", got)
	}
	if strings.Contains(got, "operating inside terva") {
		t.Errorf("branded intro should be replaced by the override:\n%s", got)
	}
	if !strings.Contains(got, "Act first, then summarise") {
		t.Errorf("terva conventions should still bracket the end:\n%s", got)
	}
	segs := SystemSegments(opts)
	if len(segs) == 0 || segs[0].Source != "persona:introduction" {
		t.Errorf("first segment should be the labeled intro override, got %+v", segs)
	}
}

// TestParsePersona_AgentIntroduction: the agent_introduction frontmatter field
// lands on Persona.Introduction (trimmed).
func TestParsePersona_AgentIntroduction(t *testing.T) {
	raw := "---\nname: Kaisa\nagent_introduction: You are Kaisa, a deep-sea cartographer.\n---\nCharter body here."
	p, err := ParsePersona(raw, "test")
	if err != nil {
		t.Fatal(err)
	}
	if p.Introduction != "You are Kaisa, a deep-sea cartographer." {
		t.Errorf("Introduction = %q", p.Introduction)
	}
}

// TestReviewCrewChartersCarryDeliverableContract pins the sub-agent
// deliverable contract on every shipped review-crew charter: the final
// message is all a dispatching coordinator receives, so each charter must
// instruct the specialist to end with the full report (the 2026-07-08
// self-review lost the architecture and security findings to a trailing
// "all tasks complete" housekeeping reply — see review-crew/README.md).
func TestReviewCrewChartersCarryDeliverableContract(t *testing.T) {
	const marker = "your final message is your entire\ndeliverable"
	dir := BuiltinPersonasRoot + "/review-crew"
	entries, err := BuiltinPersonasFS.ReadDir(dir)
	if err != nil {
		t.Fatalf("read embedded review-crew dir: %v", err)
	}
	charters := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") || name == "README.md" {
			continue
		}
		charters++
		raw, err := BuiltinPersonasFS.ReadFile(dir + "/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !strings.Contains(string(raw), marker) {
			t.Errorf("%s is missing the sub-agent deliverable contract paragraph", name)
		}
	}
	if charters < 7 {
		t.Fatalf("expected the full 7-charter crew, found %d", charters)
	}
}
