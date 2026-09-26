package core

import (
	"testing"

	"terva.sh/terva/packages/provider"
)

// After a compaction, a resume must seed the gauge with the same number the
// live agent re-baselined to (compact.go: SetLastTurn(estimateTokens(next))),
// or the gauge jumps when a session is reopened. The fold is shared by every
// store, so this pins it to the estimator here, where estimateTokens is
// visible; packages/session holds the JSONL file's round trip to this store.
// Asserted against estimateTokens rather than a literal, so the two cannot
// drift apart if the heuristic is ever retuned.
func TestMemoryStoreGaugeIsTheLiveRebaseline(t *testing.T) {
	s := NewMemoryTranscriptStore()
	turn := provider.Usage{InputTokens: 50_000}
	if err := s.AppendUsage(UsageRecord{Kind: UsageTurn, Usage: turn, Cumulative: turn}); err != nil {
		t.Fatal(err)
	}
	kept := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "## Context Summary\nwe decided on the tri-state"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "acknowledged"}}},
	}
	if err := s.AppendCompaction(kept, CompactResult{Usage: provider.Usage{InputTokens: 40_000}}); err != nil {
		t.Fatal(err)
	}
	if got, want := s.Transcript().ResumeContext.InputTokens, estimateTokens(kept); got != want || want == 0 {
		t.Errorf("resumed gauge = %d, live re-baseline = %d", got, want)
	}
}
