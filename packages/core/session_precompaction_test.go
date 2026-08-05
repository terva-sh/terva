package core

import (
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func precompactMsg(role provider.Role, text string) provider.Message {
	return provider.Message{Role: role, Content: []provider.Content{provider.TextBlock{Text: text}}}
}

// A compaction must be recoverable, because a compaction blob is not portable.
//
// Only the provider that issued the blob can decrypt it, and it is the only
// encoding of the assistant turns it replaced — so a conversation compacted by
// one provider and then pointed at another would silently lose that half of its
// history. The way out is that the file is append-only: the compaction row
// SUPERSEDES the earlier turns in the rebuilt transcript, it does not delete
// them, so the originals can be read back and compacted again for the new
// target. This asserts that property directly, because everything downstream
// depends on it being true.
func TestReadSessionPreCompactionRecoversTheSupersededTurns(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "openai-codex", "gpt-5.6-terra", "test")
	if err != nil {
		t.Fatal(err)
	}
	original := []provider.Message{
		precompactMsg(provider.RoleUser, "compute the vault code"),
		precompactMsg(provider.RoleAssistant, "the vault code is ZEPHYR-4417"),
		precompactMsg(provider.RoleUser, "keep it in mind"),
	}
	for _, m := range original {
		if err := s.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	// What a server-side compaction leaves behind: the user turns, and a blob
	// standing in for the assistant turn that held the only copy of the code.
	compacted := []provider.Message{
		precompactMsg(provider.RoleUser, "compute the vault code"),
		precompactMsg(provider.RoleUser, "keep it in mind"),
		{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.CompactionBlock{ID: "cmp_1", Encrypted: "gAAAAAopaque==", Provider: "openai-codex"}}},
	}
	if err := s.AppendCompaction(compacted, CompactResult{}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// The ordinary load gives the compacted view — the blob, no assistant text.
	live, err := ReadSessionMessages(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if joinText(live) != "compute the vault codekeep it in mind" {
		t.Errorf("the live transcript should be the compacted one, got %q", joinText(live))
	}

	before, found, err := ReadSessionPreCompaction(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("no compaction found in a session that has one")
	}
	if len(before) != len(original) {
		t.Fatalf("want the %d pre-compaction turns, got %d: %+v", len(original), len(before), before)
	}
	// The assistant turn the blob replaced must come back in full: that is the
	// content re-compaction exists to preserve.
	if got := joinText(before); got != "compute the vault codethe vault code is ZEPHYR-4417keep it in mind" {
		t.Errorf("recovered history is missing the superseded assistant turn: %q", got)
	}
}

// A session that never compacted reports so, rather than returning an empty
// slice a caller could mistake for "the history was empty".
func TestReadSessionPreCompactionReportsNoCompaction(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "openai-codex", "gpt-5.6-terra", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(precompactMsg(provider.RoleUser, "hello")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	before, found, err := ReadSessionPreCompaction(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Errorf("reported a compaction in a session with none: %+v", before)
	}
}

func joinText(msgs []provider.Message) string {
	out := ""
	for _, m := range msgs {
		for _, c := range m.Content {
			if tb, ok := c.(provider.TextBlock); ok {
				out += tb.Text
			}
		}
	}
	return out
}
