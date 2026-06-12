package chat

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

func TestFormatStatusIncludesModelUsageContextAndCWD(t *testing.T) {
	got := FormatStatus(StatusSnapshot{
		Provider:     "openai",
		Model:        "gpt-5.5",
		CWD:          "/tmp/terva",
		Usage:        provider.Usage{InputTokens: 961_000, OutputTokens: 10_000, CacheReadTokens: 770_000, CostUSD: 2.749},
		Subscription: true,
		ContextUsed:  44_800,
		ContextMax:   400_000,
		Busy:         true,
		Queued:       2,
	})

	wants := []string{
		"(openai) gpt-5.5",
		"↑961k",
		"↓10k",
		"R770k",
		"$2.749 (sub)",
		"11.2%/400k",
		"state: working",
		"queued: 2",
		"cwd: /tmp/terva",
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Fatalf("FormatStatus missing %q in:\n%s", want, got)
		}
	}
}
