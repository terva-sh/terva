package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// Skill authoring is an edit-and-retry loop: write a SKILL.md, then ask the
// model to use it. ReloadSkills exists so that loop needs no relaunch — it
// re-runs discovery and swaps the result into the session's live skill tool.
//
// But every Resolve mints a FRESH skills.Tool into the registry, and
// rebuildTools installs that registry on the agent. The session's own pointer
// (s.skillTool) is bound once at session build and never re-bound — there is
// no UseSkills() the way there is UseTasks/UseFiles/UseMemory. So after any
// rebuild the two parties disagree: the model calls the registry's tool, while
// ReloadSkills writes into the orphan the host still holds.
//
// A rebuild is not exotic. It fires when extensions finish loading, when an
// extension asserts its tool policy, on entering plan mode, on a trust flip.
// In a session with extensions it happens before the first turn — which is
// exactly what the reported incident showed ("prompt rebuilt (extensions-ready,
// scope=both) before first turn"), followed by `skill: no skill named ...` for
// a file that was sitting correctly on disk.

func skillSession(t *testing.T) (*Workspace, *wsSession, string, string) {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")

	// WithSkills is the interactive default (build.Args Defaults), and it gates
	// the $TERVA_HOME/skills rung this fixture writes into. A literal Args{}
	// leaves it false, which loads built-ins only.
	w, err := NewWorkspace(build.Args{
		Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t),
		NoExt: true, NoMCP: true, WithSkills: true,
	}, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	s := w.existing(info.ID)
	if s == nil {
		t.Fatal("session vanished after create")
	}
	if s.liveSkillTool() == nil {
		t.Fatal("session has no skill tool (built-in skills should always give it one)")
	}
	return w, s, info.ID, home
}

// writeGlobalSkill drops a SKILL.md into the $TERVA_HOME/skills rung, which
// loads regardless of the workspace trust verdict — so this test measures the
// reload seam and not the trust gate.
func writeGlobalSkill(t *testing.T, home, name string) {
	t.Helper()
	writeGlobalSkillFull(t, home, name, "a fixture skill", "Do the thing.")
}

// writeGlobalSkillFull spells out the two halves that matter to the reload
// decision: the description rides in the prompt manifest, the body does not.
func writeGlobalSkillFull(t *testing.T, home, name, description, body string) {
	t.Helper()
	dir := filepath.Join(home, "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	doc := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(doc), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

func removeGlobalSkill(t *testing.T, home, name string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(home, "skills", name)); err != nil {
		t.Fatalf("remove skill %s: %v", name, err)
	}
}

// loadSkill drives the tool the MODEL calls, reached through the agent's live
// registry — not the skills.Tool the host happens to hold. That distinction is
// the whole bug.
func loadSkill(t *testing.T, s *wsSession, name string) (string, bool) {
	t.Helper()
	tl, ok := s.agent.LookupTool("skill")
	if !ok {
		t.Fatal("the skill tool is not in the agent's registry")
	}
	res, err := tl.Execute(context.Background(), []byte(`{"name":"`+name+`"}`), func(string) {})
	if err != nil {
		t.Fatalf("skill(%q): %v", name, err)
	}
	return toolText(res), !res.IsError
}

func TestASkillWrittenAfterARebuildIsLoadableByTheModel(t *testing.T) {
	w, s, id, home := skillSession(t)

	// The startup rebuild the incident reported. Everything below is the
	// ordinary authoring loop that follows it.
	s.rebuildTools("extensions-ready")

	writeGlobalSkill(t, home, "sys-health")

	// Discovery itself is not in question: the picker rescans disk every call
	// and would show this skill either way. Pin that, so a failure below can
	// only be the swap landing on an orphaned tool.
	if found := w.ReloadSkills(id); skills.FindByName(found, "sys-health") == nil {
		t.Fatalf("precondition: discovery did not see the new skill at all")
	}

	if text, ok := loadSkill(t, s, "sys-health"); !ok {
		t.Errorf("the model cannot load a skill that ReloadSkills just discovered: %s\n"+
			"ReloadSkills swapped the catalog into a skills.Tool the agent no longer holds — "+
			"rebuildTools replaced the registry's copy and left s.skillTool pointing at the old one", text)
	}
}

// The cheap completion source reads s.skillTool directly rather than rescanning
// disk, so it drifts the same way: after a rebuild, `/skill <tab>` stops
// offering anything written this session.
func TestSessionSkillsTracksTheLiveCatalogAcrossARebuild(t *testing.T) {
	w, s, id, home := skillSession(t)

	s.rebuildTools("extensions-ready")
	writeGlobalSkill(t, home, "sys-health")
	w.ReloadSkills(id)

	if skills.FindByName(w.SessionSkills(id), "sys-health") == nil {
		t.Error("SessionSkills does not show a reloaded skill, so /skill completions " +
			"are reading a catalog the reload can no longer reach")
	}
}

// ---- /reload-skills: the conditional prompt rebuild ----
//
// Making a skill LOADABLE is free (swap the catalog). Making the model AWARE of
// it means changing the system prompt, which throws away the provider's cached
// request prefix. So /reload-skills pays that only when the manifest actually
// changed — which is what the two tests below pin from either side.

func TestANewSkillRebuildsThePromptSoTheModelCanSeeIt(t *testing.T) {
	w, s, id, home := skillSession(t)

	// A completed turn, so the notice carries the token count rather than the
	// pre-first-turn wording. skill-reload is user-initiated and so is never
	// suppressed as startup noise either way.
	s.agent.SeedLastTurnUsage(provider.Usage{InputTokens: 5000})
	sub := s.hub.add(nil, true)

	writeGlobalSkill(t, home, "sys-health")
	stats := w.ReloadSkillsAndPrompt(id)

	if !stats.PromptRebuilt {
		t.Error("a brand-new skill did not rebuild the prompt, so the model is never told it exists")
	}
	if len(stats.Added) != 1 || !strings.Contains(stats.Added[0], "sys-health") {
		t.Errorf("Added = %v, want the one new skill", stats.Added)
	}
	if len(stats.Removed) != 0 {
		t.Errorf("Removed = %v, want none", stats.Removed)
	}
	if !strings.Contains(s.agent.System, "sys-health") {
		t.Error("the rebuilt system prompt does not list the new skill")
	}

	ev, _ := drainUntil(t, sub, ctrlproto.EventNotice)
	if ev.Notice == nil || ev.Notice.Kind != ctrlproto.NoticePromptRebuilt {
		t.Fatalf("want a %s notice, got %+v", ctrlproto.NoticePromptRebuilt, ev.Notice)
	}
	if got := ev.Notice.Data["reason"]; got != "skill-reload" {
		t.Errorf("rebuild reason = %q, want skill-reload", got)
	}
}

// The common beat of the authoring loop: tweak the instructions, retry. The
// manifest is unchanged (same name, same description), so this must cost the
// user nothing — while still serving the model the NEW text on the next load.
func TestEditingASkillBodyDoesNotCostThePromptCache(t *testing.T) {
	w, s, id, home := skillSession(t)

	writeGlobalSkillFull(t, home, "sys-health", "check the host", "First draft.")
	if !w.ReloadSkillsAndPrompt(id).PromptRebuilt {
		t.Fatal("precondition: adding the skill should have rebuilt once")
	}
	systemBefore := s.agent.System

	writeGlobalSkillFull(t, home, "sys-health", "check the host", "Second draft, much better.")
	stats := w.ReloadSkillsAndPrompt(id)

	if stats.PromptRebuilt {
		t.Error("editing only the body rebuilt the prompt — that discards the request-prefix " +
			"cache on every save, which is the whole cost this path exists to avoid")
	}
	if s.agent.System != systemBefore {
		t.Error("the system prompt changed even though the manifest did not")
	}
	if len(stats.Added) != 0 || len(stats.Removed) != 0 {
		t.Errorf("an in-place edit reported Added=%v Removed=%v, want neither", stats.Added, stats.Removed)
	}

	// The point of reloading at all: the model gets the rewritten instructions.
	text, ok := loadSkill(t, s, "sys-health")
	if !ok {
		t.Fatalf("the model can no longer load the skill: %s", text)
	}
	if !strings.Contains(text, "Second draft") {
		t.Errorf("the model still gets the stale body — saving the cache must not mean "+
			"serving old instructions. Got: %s", text)
	}
}

// A description IS manifest text: it is how the model decides whether a skill
// applies. Changing it has to reach the prompt, body-edit economy or not.
func TestChangingADescriptionRebuildsBecauseTheModelReadsIt(t *testing.T) {
	w, _, id, home := skillSession(t)

	writeGlobalSkillFull(t, home, "sys-health", "check the host", "Body.")
	w.ReloadSkillsAndPrompt(id)

	writeGlobalSkillFull(t, home, "sys-health", "audit disk, memory and load", "Body.")
	if !w.ReloadSkillsAndPrompt(id).PromptRebuilt {
		t.Error("a changed description did not reach the prompt, so the model keeps " +
			"choosing the skill by its old summary")
	}
}

func TestADeletedSkillIsReportedAndLeavesThePrompt(t *testing.T) {
	w, s, id, home := skillSession(t)

	writeGlobalSkill(t, home, "sys-health")
	w.ReloadSkillsAndPrompt(id)

	removeGlobalSkill(t, home, "sys-health")
	stats := w.ReloadSkillsAndPrompt(id)

	if !stats.PromptRebuilt {
		t.Error("deleting a skill did not rebuild the prompt, so the model still sees it listed")
	}
	if len(stats.Removed) != 1 || !strings.Contains(stats.Removed[0], "sys-health") {
		t.Errorf("Removed = %v, want the deleted skill", stats.Removed)
	}
	if strings.Contains(s.agent.System, "sys-health") {
		t.Error("the deleted skill is still listed in the system prompt")
	}
	if _, ok := loadSkill(t, s, "sys-health"); ok {
		t.Error("the model can still load a skill whose file is gone")
	}
}

// The reload must keep working across REPEATED rebuilds, not just the first —
// a session flips approval mode, toggles MCP, or trusts a workspace many times.
func TestTheSkillCatalogSurvivesRepeatedRebuilds(t *testing.T) {
	w, s, id, home := skillSession(t)

	for i, reason := range []string{"extensions-ready", "approval-mode", "trust"} {
		s.rebuildTools(reason)
		name := "fixture-" + string(rune('a'+i))
		writeGlobalSkill(t, home, name)
		w.ReloadSkills(id)
		if text, ok := loadSkill(t, s, name); !ok {
			t.Fatalf("after a %q rebuild the model cannot load %q: %s", reason, name, text)
		}
	}
}
