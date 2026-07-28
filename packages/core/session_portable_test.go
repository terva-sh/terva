package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// TestSessionExportImportRoundTrip writes a few messages to a live
// session, exports it, imports the export under a different cwd,
// and verifies OpenSession on the imported file yields the same
// message payloads.
func TestSessionExportImportRoundTrip(t *testing.T) {
	root := testsupport.TempDir(t)
	originalCWD := "/path/to/project"
	sess, err := NewSession(root, originalCWD, "anthropic", "claude-opus-4-7", "0.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	_ = sess.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "hello from the exporter"}},
	})
	_ = sess.AppendMessage(provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "hi — reply from the assistant"}},
	})
	_ = sess.Close()

	// Export to a directory — helper should build a name inside it.
	exportDir := testsupport.TempDir(t)
	exportPath, err := ExportSession(sess.Path, exportDir)
	if err != nil {
		t.Fatalf("ExportSession: %v", err)
	}
	if !strings.HasSuffix(exportPath, PortableExt) {
		t.Errorf("exported path should end in %s, got %q", PortableExt, exportPath)
	}
	if _, err := os.Stat(exportPath); err != nil {
		t.Fatalf("exported file doesn't exist: %v", err)
	}

	// Import into a different root + cwd.
	root2 := testsupport.TempDir(t)
	cwd2 := "/some/other/project"
	importedPath, err := ImportSession(exportPath, root2, cwd2, "0.0.0-test")
	if err != nil {
		t.Fatalf("ImportSession: %v", err)
	}
	if filepath.Dir(importedPath) != SessionsDir(root2, cwd2) {
		t.Errorf("imported file should land in SessionsDir, got %q", importedPath)
	}

	// Reopen and verify message round-trip.
	imported, msgs, err := OpenSession(importedPath)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer imported.Close()
	if imported.Meta.CWD != cwd2 {
		t.Errorf("meta cwd: want %q, got %q", cwd2, imported.Meta.CWD)
	}
	if imported.Meta.ID == sess.ID {
		t.Errorf("imported session kept the original id %q; must be rotated", sess.ID)
	}
	if imported.Meta.Model != "claude-opus-4-7" {
		t.Errorf("model not preserved: %q", imported.Meta.Model)
	}
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages, got %d", len(msgs))
	}
	// Text should round-trip.
	if extractText(msgs[0]) != "hello from the exporter" {
		t.Errorf("msg 0 mismatch: %q", extractText(msgs[0]))
	}
	if extractText(msgs[1]) != "hi — reply from the assistant" {
		t.Errorf("msg 1 mismatch: %q", extractText(msgs[1]))
	}
}

// TestExportToFilePath writes to an explicit file path (no
// directory guessing) and checks the .tervasession extension is
// appended when missing.
func TestExportToFilePath(t *testing.T) {
	root := testsupport.TempDir(t)
	sess, err := NewSession(root, "/cwd", "anthropic", "claude-opus-4-7", "0.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	_ = sess.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "x"}},
	})
	_ = sess.Close()

	// No extension — should add .tervasession.
	dst := filepath.Join(testsupport.TempDir(t), "mysession")
	out, err := ExportSession(sess.Path, dst)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, PortableExt) {
		t.Errorf("want .tervasession suffix on %q", out)
	}
}

// TestExportStripsCWDFromMeta verifies the exported meta no longer
// carries the source user's cwd (not useful to the recipient).
func TestExportSessionHandlesHugeJSONLRows(t *testing.T) {
	root := testsupport.TempDir(t)
	sess, err := NewSession(root, "/cwd", "anthropic", "claude-opus-4-7", "0.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	_ = sess.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "first prompt names export"}},
	})
	_ = sess.AppendMessage(provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: strings.Repeat("x", 22*1024*1024)}},
	})
	_ = sess.Close()

	exportDir := testsupport.TempDir(t)
	out, err := ExportSession(sess.Path, exportDir)
	if err != nil {
		t.Fatalf("ExportSession with huge row: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("exported file missing: %v", err)
	}
	opened, msgs, err := OpenSession(sess.Path)
	if err != nil {
		t.Fatalf("OpenSession with huge row: %v", err)
	}
	_ = opened.Close()
	if len(msgs) != 2 {
		t.Fatalf("OpenSession messages=%d, want 2", len(msgs))
	}
	// Ensure the exported file is still readable JSONL and contains the
	// huge assistant message row, not just the meta/header.
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), strings.Repeat("x", 1024)) {
		t.Fatalf("export does not appear to contain huge assistant row")
	}
	for n, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatalf("exported line %d is invalid json: %v", n+1, err)
		}
	}
}

func TestExportStripsCWDFromMeta(t *testing.T) {
	root := testsupport.TempDir(t)
	sess, err := NewSession(root, "/original/cwd", "anthropic", "claude-opus-4-7", "0.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	_ = sess.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "x"}},
	})
	_ = sess.Close()

	out, err := ExportSession(sess.Path, filepath.Join(testsupport.TempDir(t), "x"+PortableExt))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if strings.Contains(string(b), "/original/cwd") {
		t.Errorf("exported file leaks the source cwd: %s", string(b))
	}
}

// TestBranchSessionCopiesPrefix writes several messages to a
// session, branches at message index 2 (first user + first
// assistant), and verifies the new session has exactly those two
// messages with parent + fork_point meta set.
func TestBranchSessionCopiesPrefix(t *testing.T) {
	root := testsupport.TempDir(t)
	cwd := "/project"
	parent, err := NewSession(root, cwd, "anthropic", "claude-opus-4-7", "0.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	_ = parent.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "first"}},
	})
	_ = parent.AppendMessage(provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "first reply"}},
	})
	_ = parent.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "second"}},
	})
	_ = parent.Close()

	// Branch at the first user+assistant pair (upToMessageIdx=2).
	branchPath, err := BranchSession(parent.Path, root, cwd, "0.0.0-test", 2)
	if err != nil {
		t.Fatalf("BranchSession: %v", err)
	}
	branch, msgs, err := OpenSession(branchPath)
	if err != nil {
		t.Fatal(err)
	}
	defer branch.Close()

	if len(msgs) != 2 {
		t.Errorf("want 2 copied messages, got %d", len(msgs))
	}
	if branch.Meta.Parent != parent.Meta.ID {
		t.Errorf("parent id: want %q, got %q", parent.Meta.ID, branch.Meta.Parent)
	}
	if branch.Meta.ForkPoint != 2 {
		t.Errorf("fork_point: want 2, got %d", branch.Meta.ForkPoint)
	}
	if branch.Meta.ID == parent.Meta.ID {
		t.Errorf("branch kept parent id; must rotate")
	}
}

func TestBranchSessionUsesEffectiveTranscriptAfterCompaction(t *testing.T) {
	root := testsupport.TempDir(t)
	cwd := "/project"
	parent, err := NewSession(root, cwd, "anthropic", "claude-opus-4-7", "0.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"raw-a", "raw-b", "raw-c", "raw-d"} {
		_ = parent.AppendMessage(provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: text}},
		})
	}
	_ = parent.AppendCompaction([]provider.Message{
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "summary"}}},
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "tail-c"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "tail-d"}}},
	}, CompactResult{})
	_ = parent.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "after-compact"}},
	})
	_ = parent.Close()

	opened, msgs, err := OpenSession(parent.Path)
	if err != nil {
		t.Fatalf("OpenSession parent: %v", err)
	}
	_ = opened.Close()
	assertMessageTexts(t, msgs, []string{"summary", "tail-c", "tail-d", "after-compact"})

	branchPath, err := BranchSession(parent.Path, root, cwd, "0.0.0-test", 4)
	if err != nil {
		t.Fatalf("BranchSession: %v", err)
	}
	branch, branchMsgs, err := OpenSession(branchPath)
	if err != nil {
		t.Fatalf("OpenSession branch: %v", err)
	}
	defer branch.Close()

	assertMessageTexts(t, branchMsgs, []string{"summary", "tail-c", "tail-d", "after-compact"})
	if branch.Meta.Parent != parent.Meta.ID {
		t.Errorf("parent id: want %q, got %q", parent.Meta.ID, branch.Meta.Parent)
	}
	if branch.Meta.ForkPoint != 4 {
		t.Errorf("fork_point: want 4, got %d", branch.Meta.ForkPoint)
	}

	shortBranchPath, err := BranchSession(parent.Path, root, cwd, "0.0.0-test", 2)
	if err != nil {
		t.Fatalf("BranchSession short fork: %v", err)
	}
	shortBranch, shortBranchMsgs, err := OpenSession(shortBranchPath)
	if err != nil {
		t.Fatalf("OpenSession short branch: %v", err)
	}
	defer shortBranch.Close()

	assertMessageTexts(t, shortBranchMsgs, []string{"summary", "tail-c"})
	if shortBranch.Meta.ForkPoint != 2 {
		t.Errorf("short branch fork_point: want 2, got %d", shortBranch.Meta.ForkPoint)
	}
}

func assertMessageTexts(t *testing.T, msgs []provider.Message, want []string) {
	t.Helper()
	if len(msgs) != len(want) {
		t.Fatalf("message count: want %d, got %d", len(want), len(msgs))
	}
	for i, msg := range msgs {
		if got := extractText(msg); got != want[i] {
			t.Errorf("message %d: want %q, got %q", i, want[i], got)
		}
	}
}

// TestBuildSessionTree verifies parent/child edges are rebuilt
// from meta + sibling-scan.
func TestBuildSessionTree(t *testing.T) {
	root := testsupport.TempDir(t)
	cwd := "/project"
	parent, _ := NewSession(root, cwd, "anthropic", "claude-opus-4-7", "0.0.0-test")
	_ = parent.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "x"}},
	})
	_ = parent.Close()

	childA, err := BranchSession(parent.Path, root, cwd, "0.0.0-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	childB, err := BranchSession(parent.Path, root, cwd, "0.0.0-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = childA
	_ = childB

	tree := BuildSessionTree(root, cwd)
	if len(tree) != 1 {
		t.Fatalf("want 1 root, got %d", len(tree))
	}
	rootNode := tree[0]
	if rootNode.Meta.ID != parent.Meta.ID {
		t.Errorf("root should be parent %q, got %q", parent.Meta.ID, rootNode.Meta.ID)
	}
	if len(rootNode.Children) != 2 {
		t.Errorf("want 2 children, got %d", len(rootNode.Children))
	}
}

// A session's state does not live in its first meta row. Meta rows are an
// append-only, last-wins timeline: SetCreationSpec writes the SECOND one, and
// everything a Stage session is — its mode, card, cast, greeting, lorebook,
// note, background, bound user — is written after creation, as is every model
// switch. A round trip that keeps only the first row therefore hands back a
// session's birth certificate instead of the session.
//
// This is the test that was missing: the suite exercised messages, which are
// non-meta rows and were always streamed, so the whole meta timeline could be
// dropped by both sides without a single failure.
func TestExportImportPreservesTheWholeMetaTimeline(t *testing.T) {
	root := testsupport.TempDir(t)
	sess, err := NewSession(root, "/path/to/project", "anthropic", "claude-opus-4-7", "0.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	// Each of these appends its own meta row; none of them is the first.
	if err := sess.SetCreationSpec("kobeni", "play", "card_123", map[string]string{"Ada": "card_ada"}, 2); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetWorldLore([]WorldLoreEntry{{Name: "The Vault", Content: "sealed since the war", Constant: true}}); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetNote("keep it tense"); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetBackground("bg_rain"); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetUserPersona("Mara", "a courier", "", "she/her"); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetCoordination("focus:Ada"); err != nil {
		t.Fatal(err)
	}
	if err := sess.UpdateModel("openai", "gpt-5"); err != nil {
		t.Fatal(err)
	}
	_ = sess.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "hello"}},
	})
	_ = sess.Close()

	before, _, err := OpenSession(sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	_ = before.Close()

	exportPath, err := ExportSession(sess.Path, testsupport.TempDir(t))
	if err != nil {
		t.Fatalf("ExportSession: %v", err)
	}
	imported, err := ImportSession(exportPath, testsupport.TempDir(t), "/somewhere/else", "0.0.0-test")
	if err != nil {
		t.Fatalf("ImportSession: %v", err)
	}
	after, _, err := OpenSession(imported)
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()

	a, b := before.Meta, after.Meta
	for _, c := range []struct {
		field     string
		got, want any
	}{
		{"Experience", b.Experience, a.Experience},
		{"Card", b.Card, a.Card},
		{"Greeting", b.Greeting, a.Greeting},
		{"Persona", b.Persona, a.Persona},
		{"Note", b.Note, a.Note},
		{"Background", b.Background, a.Background},
		{"UserName", b.UserName, a.UserName},
		{"UserDescription", b.UserDescription, a.UserDescription},
		{"UserPronouns", b.UserPronouns, a.UserPronouns},
		{"Coordination", b.Coordination, a.Coordination},
		{"Model", b.Model, a.Model},
		{"Provider", b.Provider, a.Provider},
		{"len(Cast)", len(b.Cast), len(a.Cast)},
		{"len(WorldLore)", len(b.WorldLore), len(a.WorldLore)},
	} {
		if c.got != c.want {
			t.Errorf("%s = %v after the round trip, want %v", c.field, c.got, c.want)
		}
	}
	if len(b.WorldLore) == 1 && b.WorldLore[0].Content != "sealed since the war" {
		t.Errorf("lore entry survived by name but lost its content: %+v", b.WorldLore[0])
	}
	if b.Cast["Ada"] != "card_ada" {
		t.Errorf("cast = %v, want Ada -> card_ada", b.Cast)
	}
}

// The export must not carry the exporting user's working directory — not in
// the first meta row and not in any later one, which is the reason later rows
// were being dropped rather than an argument for dropping them.
func TestExportStripsTheCWDFromEveryMetaRow(t *testing.T) {
	root := testsupport.TempDir(t)
	sess, err := NewSession(root, "/home/someone/secret-project", "anthropic", "m", "0.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.SetNote("later row"); err != nil {
		t.Fatal(err)
	}
	if err := sess.SetBackground("bg"); err != nil {
		t.Fatal(err)
	}
	_ = sess.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "hi"}},
	})
	_ = sess.Close()

	exportPath, err := ExportSession(sess.Path, testsupport.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(exportPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "secret-project") {
		t.Error("the export names the exporting user's cwd")
	}
	// And prove the later rows are actually there to have been stripped, or the
	// assertion above passes for the wrong reason.
	var metaRows int
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		var h sessionLineHead
		if json.Unmarshal([]byte(line), &h) == nil && h.Type == "meta" {
			metaRows++
		}
	}
	if metaRows < 3 {
		t.Errorf("export carries %d meta row(s); the source had a creation row plus a note and a background", metaRows)
	}
}

// Replaying the source's meta rows must not replay its IDENTITY. Each row
// carries id, cwd, started, version and possibly a parent — all of which
// describe the exporting user's session, not this copy of it. Getting this
// wrong is worse than the bug it fixes: a last-wins row would quietly hand the
// imported session the original's id and another machine's path.
func TestImportKeepsItsOwnIdentityAcrossEveryReplayedRow(t *testing.T) {
	root := testsupport.TempDir(t)
	parent, err := NewSession(root, "/original/cwd", "anthropic", "m", "0.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	// A forked session: Parent names a branch that will not be imported.
	if err := parent.SetParent("some-other-session-id"); err != nil {
		t.Fatal(err)
	}
	if err := parent.SetNote("a later row"); err != nil {
		t.Fatal(err)
	}
	_ = parent.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "hi"}},
	})
	originalID := parent.Meta.ID
	_ = parent.Close()

	exportPath, err := ExportSession(parent.Path, testsupport.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	imported, err := ImportSession(exportPath, testsupport.TempDir(t), "/my/cwd", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	s, _, err := OpenSession(imported)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if s.Meta.ID == originalID {
		t.Error("the imported session kept the original's id — a replayed meta row won last")
	}
	if s.Meta.CWD != "/my/cwd" {
		t.Errorf("CWD = %q, want the importing user's", s.Meta.CWD)
	}
	if s.Meta.Version != "1.2.3" {
		t.Errorf("Version = %q, want the importing build's", s.Meta.Version)
	}
	if s.Meta.Parent != "" {
		t.Errorf("Parent = %q — it names a session that was not imported", s.Meta.Parent)
	}
	// …while the state on those same rows still arrived.
	if s.Meta.Note != "a later row" {
		t.Errorf("Note = %q, want the state from the replayed row", s.Meta.Note)
	}
	// And no row anywhere in the file names the exporter's directory.
	body, err := os.ReadFile(imported)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "/original/cwd") {
		t.Error("the imported file names the exporting user's cwd")
	}
}
