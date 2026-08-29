package modes

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
)

// newReloadSkillsInteractive is the smallest Interactive that can dispatch
// /reload-skills: the handler only reads its config closure and writes a status
// line.
func newReloadSkillsInteractive(reload func() SkillReload) *Interactive {
	return &Interactive{
		turns: newTurnEngine(),
		dirty: make(chan struct{}, 1),
		cfg: InteractiveConfig{
			Theme:                 tui.Dark,
			ReloadSkillsAndPrompt: reload,
		},
	}
}

// A new skill costs the user their prompt cache. Saying only "reloaded" would
// hide a real cost they just paid, and leave the difference between a free
// reload and an expensive one invisible.
func TestReloadSkillsReportsTheRebuildItPaidFor(t *testing.T) {
	i := newReloadSkillsInteractive(func() SkillReload {
		return SkillReload{
			Available:     12,
			Added:         []string{"terva:sys-health"},
			PromptRebuilt: true,
		}
	})

	i.runReloadSkills()

	if i.statusErr != "" {
		t.Fatalf("unexpected error status: %q", i.statusErr)
	}
	if !strings.Contains(i.statusOK, "12") {
		t.Errorf("status does not report how many skills are available: %q", i.statusOK)
	}
	if !strings.Contains(i.statusOK, "sys-health") {
		t.Errorf("status does not name the skill that appeared: %q", i.statusOK)
	}
	if !strings.Contains(i.statusOK, "uncached") {
		t.Errorf("status does not disclose that the next turn starts uncached: %q", i.statusOK)
	}
}

// The common authoring beat: edit a body, reload, retry. Nothing was added and
// no cache was dropped, so the status must not claim a cost that was not paid.
func TestReloadSkillsStaysQuietWhenNothingChanged(t *testing.T) {
	i := newReloadSkillsInteractive(func() SkillReload {
		return SkillReload{Available: 12}
	})

	i.runReloadSkills()

	if !strings.Contains(i.statusOK, "12") {
		t.Errorf("status does not confirm the reload happened: %q", i.statusOK)
	}
	if strings.Contains(i.statusOK, "uncached") {
		t.Errorf("a reload that rebuilt nothing still claimed the next turn is uncached: %q", i.statusOK)
	}
	for _, unwanted := range []string{"added", "removed"} {
		if strings.Contains(i.statusOK, unwanted) {
			t.Errorf("status mentions %q with an empty list: %q", unwanted, i.statusOK)
		}
	}
}

func TestReloadSkillsReportsARemovedSkill(t *testing.T) {
	i := newReloadSkillsInteractive(func() SkillReload {
		return SkillReload{Available: 11, Removed: []string{"terva:sys-health"}, PromptRebuilt: true}
	})

	i.runReloadSkills()

	if !strings.Contains(i.statusOK, "removed") || !strings.Contains(i.statusOK, "sys-health") {
		t.Errorf("status does not report the skill that went away: %q", i.statusOK)
	}
}

// A session with no skill source (--no-skill) must degrade to a note rather
// than panic on the nil closure.
func TestReloadSkillsWithoutASkillSourceDegrades(t *testing.T) {
	i := newReloadSkillsInteractive(nil)

	i.runReloadSkills()

	if i.statusErr == "" {
		t.Error("/reload-skills with no skill source said nothing; it should explain why")
	}
	if i.statusOK != "" {
		t.Errorf("a failed reload reported success: %q", i.statusOK)
	}
}
