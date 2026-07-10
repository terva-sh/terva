package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

// A native additive Persona's agent_introduction must survive a mid-session
// system-prompt rebuild. Regression: Resolve stored only the CARD's intro
// override on Resolved, and rebuildSystemPrompt dropped the Persona fallback (and
// its provenance label), so any extension that merged tools/context silently
// reverted the Persona's identity paragraph to the branded default.
func TestResolvePersonaIntroSurvivesToolMerge(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	if err := config.SaveConfig(config.Config{Provider: "openai", Model: "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	dir := testsupport.TempDir(t)
	personaFile := filepath.Join(dir, "aria.md")
	const intro = "I am Aria, keeper of the deep-water lantern."
	body := "---\nname: Aria\nagent_introduction: " + intro + "\n---\nAria is a deep-sea oracle.\n"
	if err := os.WriteFile(personaFile, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := build.Resolve(build.Args{CWD: dir, Persona: personaFile}, false)
	if err != nil {
		t.Fatal(err)
	}

	// Resolve time: the Persona's agent_introduction is the labeled intro slot.
	if !strings.Contains(r.SystemPrompt, intro) {
		t.Fatalf("persona agent_introduction missing from initial prompt:\n%s", r.SystemPrompt)
	}
	if len(r.SystemSegments) == 0 || r.SystemSegments[0].Source != "persona:introduction" {
		t.Fatalf("initial intro provenance wrong, got %+v", r.SystemSegments)
	}

	// A mid-session extension merge rebuilds the cached prompt (the common path:
	// every wiring path runs MergeExtensionTools before NewAgent).
	r.MergeExtensionTools(&staticCtxSource{static: "EXT-CTX-BLOCK"})
	if !strings.Contains(r.SystemPrompt, "EXT-CTX-BLOCK") {
		t.Fatalf("extension context not merged:\n%s", r.SystemPrompt)
	}
	if !strings.Contains(r.SystemPrompt, intro) {
		t.Errorf("persona intro DROPPED after extension tool merge:\n%s", r.SystemPrompt)
	}
	if len(r.SystemSegments) == 0 || r.SystemSegments[0].Source != "persona:introduction" {
		t.Errorf("intro provenance lost on rebuild, got %+v", r.SystemSegments)
	}
}
