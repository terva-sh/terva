package core

import (
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func revUser(text string) provider.Message {
	return provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: text}}}
}

func revSummary(text string) provider.Message {
	return provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: text}},
		Meta:    map[string]string{"compaction": "true"},
	}
}

// TestRevealCompaction reconstructs, from the append-only transcript, exactly
// what each compaction checkpoint summarized away — including across a second
// compaction that folded in the first summary. The originals are never rewritten,
// so this is a pure read; the reveal is positional (input minus the kept tail).
func TestRevealCompaction(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	// Turn 1..2, then a first compaction keeping the last 2, more turns, then a
	// second compaction that summarizes [summary1, m2, m3, m4, m5] keeping 2.
	for _, m := range []provider.Message{revUser("m0"), revUser("m1"), revUser("m2"), revUser("m3")} {
		if err := s.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AppendCompaction([]provider.Message{revSummary("summary1"), revUser("m2"), revUser("m3")}, CompactResult{}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []provider.Message{revUser("m4"), revUser("m5")} {
		if err := s.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AppendCompaction([]provider.Message{revSummary("summary2"), revUser("m4"), revUser("m5")}, CompactResult{}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	text := func(m provider.Message) string {
		if len(m.Content) == 0 {
			return ""
		}
		tb, _ := m.Content[0].(provider.TextBlock)
		return tb.Text
	}
	texts := func(ms []provider.Message) []string {
		out := make([]string, len(ms))
		for i, m := range ms {
			out[i] = text(m)
		}
		return out
	}

	// Latest checkpoint (ordinal 1): input was [summary1, m2, m3, m4, m5], kept 2,
	// so it summarized away [summary1, m2, m3] — and summary1 is the prior checkpoint.
	span, err := RevealCompaction(s.Path, -1)
	if err != nil {
		t.Fatalf("reveal(-1): %v", err)
	}
	if span.Total != 2 || span.Ordinal != 1 || span.PrevOrdinal != 0 || span.KeptCount != 2 {
		t.Errorf("span meta = %+v, want total=2 ordinal=1 prev=0 kept=2", span)
	}
	if got := texts(span.Replaced); len(got) != 3 || got[0] != "summary1" || got[2] != "m3" {
		t.Errorf("replaced = %v, want [summary1 m2 m3]", got)
	}
	if span.Replaced[0].Meta["compaction"] != "true" {
		t.Error("first replaced message should be the prior summary (compaction=true)")
	}

	// First checkpoint (ordinal 0): input was [m0, m1, m2, m3], kept 2 → dropped [m0, m1].
	span0, err := RevealCompaction(s.Path, 0)
	if err != nil {
		t.Fatalf("reveal(0): %v", err)
	}
	if span0.PrevOrdinal != -1 {
		t.Errorf("first checkpoint prev = %d, want -1", span0.PrevOrdinal)
	}
	if got := texts(span0.Replaced); len(got) != 2 || got[0] != "m0" || got[1] != "m1" {
		t.Errorf("replaced(0) = %v, want [m0 m1]", got)
	}

	// Out-of-range and no-compaction cases error.
	if _, err := RevealCompaction(s.Path, 9); err == nil {
		t.Error("out-of-range ordinal should error")
	}
}
