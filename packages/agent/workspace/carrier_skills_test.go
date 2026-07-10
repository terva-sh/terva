package workspace

import "testing"

// TestCarrierSkillsNilSafe pins the pre-login / no-session contract: the /skills
// picker and completions must return nil (never panic) when the bound session
// doesn't exist, rather than rescanning disk for a session that isn't there.
func TestCarrierSkillsNilSafe(t *testing.T) {
	w := &Workspace{sessions: map[string]*wsSession{}}
	if got := w.SkillSnapshot("missing"); got != nil {
		t.Errorf("SkillSnapshot(missing) = %v, want nil", got)
	}
	if got := w.ReloadSkills("missing"); got != nil {
		t.Errorf("ReloadSkills(missing) = %v, want nil", got)
	}
	if got := w.SessionSkills("missing"); got != nil {
		t.Errorf("SessionSkills(missing) = %v, want nil", got)
	}
}
