package workspace

import (
	"testing"

	"terva.sh/terva/packages/provider"
)

// The /context breakdown labels each message so an oversized tool result stands
// out. These cover ctxMessageKind/ctxMessageBytes, the live implementations the
// Context service verb uses. (They previously tested a byte-identical copy in
// packages/agent/modes, kept alive only by the TUI's nil-Carrier breakdown; the
// copy is gone, so the coverage moved onto the code that actually runs.)

// A compaction summary is a synthetic user message left by Compact. Labelling
// it "compaction" makes it visible that the transcript restarts there.
func TestCtxMessageKindCompaction(t *testing.T) {
	m := provider.Message{
		Role:    provider.RoleUser,
		Meta:    map[string]string{"compaction": "true"},
		Content: []provider.Content{provider.TextBlock{Text: "summary of earlier turns"}},
	}
	if got := ctxMessageKind(m); got != "compaction" {
		t.Errorf("ctxMessageKind(compaction summary) = %q; want compaction", got)
	}
}

func TestCtxMessageKindAndBytes(t *testing.T) {
	tr := provider.Message{
		Role: provider.RoleTool,
		Content: []provider.Content{
			provider.ToolResultBlock{CallID: "x", Content: []provider.Content{provider.TextBlock{Text: "big result"}}},
		},
	}
	if got := ctxMessageKind(tr); got != "tool_result" {
		t.Errorf("ctxMessageKind = %q; want tool_result", got)
	}
	if ctxMessageBytes(tr) <= 0 {
		t.Error("ctxMessageBytes should be > 0 for a non-empty message")
	}

	asst := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "hi"}}}
	if got := ctxMessageKind(asst); got != "assistant" {
		t.Errorf("plain assistant ctxMessageKind = %q; want assistant", got)
	}

	call := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
		provider.ToolCallBlock{ID: "c1", Name: "read"},
	}}
	if got := ctxMessageKind(call); got != "assistant+tool" {
		t.Errorf("tool-calling assistant ctxMessageKind = %q; want assistant+tool", got)
	}
}
