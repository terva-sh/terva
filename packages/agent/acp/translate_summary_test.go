//go:build terva_acp

package acp

import (
	"testing"

	"terva.sh/terva/packages/provider"
)

// A reasoning summary repaints a resumed turn only when there is no visible
// text. Enabling reasoning summaries on the codex path makes both blocks
// present at once, and splicing the model's internal reasoning into its
// answer would change what an ACP client shows on session/load.
func TestMessageTextSummaryIsFallbackNotAddition(t *testing.T) {
	withBoth := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
		provider.ReasoningBlock{ID: "rs_1", Summary: "**Weighing options**", Encrypted: "OPAQUE"},
		provider.TextBlock{Text: "the assistant answer"},
	}}
	if got := messageText(withBoth); got != "the assistant answer" {
		t.Errorf("messageText = %q, want just the visible text", got)
	}

	// The case the fallback exists for: providers whose reasoning IS the
	// whole output must still repaint rather than replaying a blank turn.
	summaryOnly := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
		provider.ReasoningBlock{Summary: "thinking only"},
	}}
	if got := messageText(summaryOnly); got != "thinking only" {
		t.Errorf("messageText = %q, want the summary when there is no text", got)
	}
}
