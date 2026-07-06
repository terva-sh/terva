package core

import (
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func TestReadReplayRows(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := NewSessionAtPath(path, "/cwd", "prov", "model", "v1")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.TextBlock{Text: "reply"},
			provider.ToolCallBlock{ID: "t1", Name: "read", Arguments: []byte(`{"path":"x"}`)},
		}},
		{Role: provider.RoleTool, Content: []provider.Content{
			provider.ToolResultBlock{CallID: "t1", Content: []provider.Content{provider.TextBlock{Text: "out"}}},
		}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "done"}}},
	}
	for _, m := range msgs {
		if err := sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := sess.AppendUsage(provider.Usage{InputTokens: 10, OutputTokens: 5}, provider.Usage{InputTokens: 10, OutputTokens: 5}); err != nil {
		t.Fatal(err)
	}

	rows, meta, err := ReadReplayRows(path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Model != "model" || meta.Provider != "prov" {
		t.Errorf("meta = %+v", meta)
	}
	want := []ReplayRowKind{ReplayRowMessage, ReplayRowMessage, ReplayRowMessage, ReplayRowMessage, ReplayRowUsage}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i, k := range want {
		if rows[i].Kind != k {
			t.Errorf("row %d kind = %s, want %s", i, rows[i].Kind, k)
		}
	}
	if rows[4].Usage.InputTokens != 10 {
		t.Errorf("usage input = %d, want 10", rows[4].Usage.InputTokens)
	}
}

// TestReadReplayRowsKeepsCompactedHistory is the whole point of the reader: it
// preserves the rows a compaction folded away, where OpenSession collapses them.
func TestReadReplayRowsKeepsCompactedHistory(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := NewSessionAtPath(path, "/cwd", "prov", "model", "v1")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	msg := func(text string) provider.Message {
		return provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: text}}}
	}
	if err := sess.AppendMessage(msg("one")); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendMessage(msg("two")); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendCompaction([]provider.Message{msg("summary")}); err != nil {
		t.Fatal(err)
	}
	if err := sess.AppendMessage(msg("three")); err != nil {
		t.Fatal(err)
	}

	rows, _, err := ReadReplayRows(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []ReplayRowKind{ReplayRowMessage, ReplayRowMessage, ReplayRowCompaction, ReplayRowMessage}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i, k := range want {
		if rows[i].Kind != k {
			t.Errorf("row %d = %s, want %s", i, rows[i].Kind, k)
		}
	}
	if len(rows[2].Checkpoint) != 1 {
		t.Errorf("checkpoint len = %d, want 1", len(rows[2].Checkpoint))
	}

	// Contrast: the loader collapses to the checkpoint + the trailing message.
	_, loaded, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Errorf("OpenSession returned %d messages, want 2 (summary + three)", len(loaded))
	}
}
