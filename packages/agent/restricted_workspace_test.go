package agent

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

// The untrusted-workspace note is a coding/workspace signal — present in normal
// mode when project content is gated, but dropped in chat/play, where it's
// immersion-breaking OOC chrome (the TUI's /trust reminder covers the user).
func TestResolve_RestrictedNoteGatedOutOfImmersive(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	if err := config.SaveConfig(config.Config{Provider: "openai", Model: "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	// An untrusted cwd with gated project content (.terva/extensions triggers
	// hasGatedProjectContent; a fresh temp dir is untrusted).
	dir := testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(dir, ".terva", "extensions"), 0o755); err != nil {
		t.Fatal(err)
	}

	hasRestricted := func(t *testing.T, exp string) bool {
		t.Helper()
		r, err := build.Resolve(build.Args{CWD: dir, Experience: exp}, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range r.SystemSegments {
			if s.Source == "restricted-workspace" {
				return true
			}
		}
		return false
	}

	if !hasRestricted(t, "") {
		t.Error("coding mode should carry the restricted-workspace note when content is gated + untrusted")
	}
	if hasRestricted(t, build.ExperienceChat) {
		t.Error("--chat must not carry the restricted-workspace note")
	}
	if hasRestricted(t, build.ExperiencePlay) {
		t.Error("--play must not carry the restricted-workspace note")
	}
}
