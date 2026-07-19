package core

import (
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// TestReadReplayRowsCheckpointSurvivesLaterAmend is the regression for the
// release-review finding: ReadReplayRows retained a compaction checkpoint slice
// that walkSession aliases as its live effective transcript, so a later amend
// (delete/replace) mutated the checkpoint's backing array in place — the session
// player then rendered a garbled compaction reset. The reader must return the
// checkpoint as it was folded.
func TestReadReplayRowsCheckpointSurvivesLaterAmend(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := NewSessionAtPath(path, "/cwd", "prov", "model", "v1")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	msg := func(text string) provider.Message {
		return provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: text}}}
	}
	text := func(m provider.Message) string {
		tb, _ := m.Content[0].(provider.TextBlock)
		return tb.Text
	}

	// A checkpoint whose output is two messages, then an amend that mutates the
	// live effective transcript AFTER it. Without the copy, deleting index 0 shifts
	// "B" into slot 0 of the shared backing array, so the checkpoint reads [B, B].
	if err := sess.AppendCompaction([]provider.Message{msg("A"), msg("B")}, CompactResult{}); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendAmend(AmendDelete, 0, nil, "test"); err != nil {
		t.Fatal(err)
	}

	rows, _, err := ReadReplayRows(path)
	if err != nil {
		t.Fatal(err)
	}
	var cp []provider.Message
	for _, r := range rows {
		if r.Kind == ReplayRowCompaction {
			cp = r.Checkpoint
		}
	}
	if len(cp) != 2 {
		t.Fatalf("checkpoint len = %d, want 2", len(cp))
	}
	if got := []string{text(cp[0]), text(cp[1])}; got[0] != "A" || got[1] != "B" {
		t.Fatalf("checkpoint = %v, want [A B] — a later amend corrupted the retained slice", got)
	}
}
