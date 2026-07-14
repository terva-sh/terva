package tui

import (
	"testing"

	"terva.sh/terva/packages/provider"
)

// TestHashMessageCoversRenderAffectingMeta pins a cache bug that cost a real
// debugging session.
//
// renderCache is keyed by a hash of the message, and that hash covered only Role and
// Content. But renderMessage branches on Meta: a compaction summary draws a rule
// instead of a user bubble, and a /clear divider draws one of two labels depending on
// whether the user has crossed it.
//
// The two states of a clear divider have IDENTICAL content — a clear leaves no
// message behind, so the divider standing for it is synthesized with none — and so
// they hashed the same. The cache handed back the pre-crossing row forever: the state
// was right, the screen was wrong, and nothing in the transcript looked amiss.
//
// The rule this encodes: anything renderMessage READS must be hashed.
func TestHashMessageCoversRenderAffectingMeta(t *testing.T) {
	clear := func(state string) provider.Message {
		return provider.Message{Role: provider.RoleUser, Meta: map[string]string{"clear": state}}
	}
	if hashMessage(clear("true")) == hashMessage(clear("crossed")) {
		t.Error("a clear divider hashes the same before and after it is crossed — the render cache will serve the stale row")
	}

	// Same trap for a compaction whose token count moves under an unchanged summary.
	compaction := func(tokens string) provider.Message {
		return provider.Message{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: "## Context Summary (compacted)\n\nsame summary"}},
			Meta:    map[string]string{"compaction": "true", "tokens_before": tokens},
		}
	}
	if hashMessage(compaction("1000")) == hashMessage(compaction("112000")) {
		t.Error("a compaction hashes the same regardless of the token count it renders")
	}

	// And the marker itself must count: a compaction summary and an ordinary user
	// message with the same text render completely differently.
	plain := provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "hello"}},
	}
	marked := plain
	marked.Meta = map[string]string{"compaction": "true"}
	if hashMessage(plain) == hashMessage(marked) {
		t.Error("a compaction divider hashes the same as the user bubble it must not render as")
	}
}
