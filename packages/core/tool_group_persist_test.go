package core

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// TestActivateGroupFiresObserverOnceForARealActivation pins the write side: a
// host persists on this observer, so a re-activation that fired again would
// append a duplicate row on every retry.
func TestActivateGroupFiresObserverOnceForARealActivation(t *testing.T) {
	a := &Agent{}
	a.EnableLazyTools()
	var fired []string
	a.AddToolGroupActivatedObserver(func(g string) { fired = append(fired, g) })

	if !a.ActivateGroup("index") {
		t.Fatal("first activation reported no change")
	}
	if a.ActivateGroup("index") {
		t.Error("second activation reported a change")
	}
	if len(fired) != 1 || fired[0] != "index" {
		t.Errorf("observer fired %v; want exactly one 'index'", fired)
	}
}

// TestRestoreActiveGroupsDoesNotFireTheObserver is the append-loop guard: the
// restored groups are already on disk, so re-firing would grow the session file
// by one row per group per resume, forever.
func TestRestoreActiveGroupsDoesNotFireTheObserver(t *testing.T) {
	a := &Agent{}
	a.EnableLazyTools()
	var fired []string
	a.AddToolGroupActivatedObserver(func(g string) { fired = append(fired, g) })

	a.RestoreActiveGroups([]string{"index", "mail"})
	if len(fired) != 0 {
		t.Errorf("restore fired the observer %v; a resume would append duplicate rows", fired)
	}
	if got := a.ActiveGroups(); len(got) != 2 {
		t.Errorf("ActiveGroups() = %v; want index+mail restored", got)
	}
}

// TestRestoreActiveGroupsReplacesRatherThanUnions covers the session SWITCH.
// Binding fires on resume/fork/new/cd too, and a group that leaked from the
// outgoing session would be advertised by a session that has no tool_group row
// for it — so its next resume would drop it and pay the invalidation this whole
// mechanism exists to prevent.
func TestRestoreActiveGroupsReplacesRatherThanUnions(t *testing.T) {
	a := &Agent{}
	a.EnableLazyTools("always")

	a.RestoreActiveGroups([]string{"from-session-a"})
	a.RestoreActiveGroups([]string{"from-session-b"})

	got := a.ActiveGroups()
	want := map[string]bool{"always": true, "from-session-b": true}
	if len(got) != len(want) {
		t.Fatalf("ActiveGroups() = %v; want exactly %v", got, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("group %q leaked across the switch; ActiveGroups() = %v", g, got)
		}
	}
}

// TestToolGroupActivationSurvivesAResume is the end-to-end property the cost
// depends on: activate, close, reopen, and the reloaded session still names the
// group — so the resumed agent advertises the same tools array the provider
// cached the transcript behind.
func TestToolGroupActivationSurvivesAResume(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "s.jsonl")
	sess, err := NewSessionAtPath(path, dir, "openai-codex", "gpt-5.6-sol", "test")
	if err != nil {
		t.Fatalf("NewSessionAtPath: %v", err)
	}
	// A session with no messages is pruned on Close, so give it one.
	if err := sess.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "hi"}},
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if err := sess.AppendToolGroupActivation("index"); err != nil {
		t.Fatalf("AppendToolGroupActivation: %v", err)
	}
	// Re-activating the same group across runs is exactly the symptom being
	// fixed; the loader must not report it twice.
	if err := sess.AppendToolGroupActivation("index"); err != nil {
		t.Fatalf("AppendToolGroupActivation (repeat): %v", err)
	}
	if err := sess.AppendToolGroupActivation("mail"); err != nil {
		t.Fatalf("AppendToolGroupActivation(mail): %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer reopened.Close()
	got := reopened.ActiveToolGroups
	if len(got) != 2 || got[0] != "index" || got[1] != "mail" {
		t.Fatalf("ActiveToolGroups = %v; want [index mail] in first-activation order", got)
	}

	// And the agent that resumes onto it advertises them again.
	a := &Agent{}
	a.EnableLazyTools()
	a.RestoreActiveGroups(reopened.ActiveToolGroups)
	if groups := a.ActiveGroups(); len(groups) != 2 {
		t.Errorf("resumed agent ActiveGroups() = %v; want index+mail", groups)
	}
}

// TestSessionWithoutToolGroupRowsResumesUnchanged pins the compatibility case:
// every session written before this row type existed must load with an empty
// set rather than a warning or a skipped row.
func TestSessionWithoutToolGroupRowsResumesUnchanged(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, "legacy.jsonl")
	sess, err := NewSessionAtPath(path, dir, "openai-codex", "gpt-5.6-sol", "test")
	if err != nil {
		t.Fatalf("NewSessionAtPath: %v", err)
	}
	if err := sess.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "hi"}},
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer reopened.Close()
	if len(reopened.ActiveToolGroups) != 0 {
		t.Errorf("ActiveToolGroups = %v; want empty for a legacy session", reopened.ActiveToolGroups)
	}
	if len(reopened.LoadWarnings) != 0 {
		t.Errorf("LoadWarnings = %v; want none", reopened.LoadWarnings)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("session file missing after reopen: %v", err)
	}
}
