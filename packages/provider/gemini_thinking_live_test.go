package provider_test

// What does Gemini actually charge for thinking at each level, and can thinking
// be turned OFF?
//
// This exists because the answer decides a fix rather than decorating one.
// terva's next-step suggestion sends a 200-token cap with reasoning explicitly
// off, and gemini.go:1152 adds thoughtsTokenCount INTO OutputTokens — so if the
// off switch does not reach the wire, thinking is billed against that cap and
// the user is handed the opening of a sentence. Measured, not assumed: the
// levels below are read back from the provider's own thoughtsTokenCount.
//
// The trick that makes this measurable before any fix exists: `minimum` already
// maps to thinkingLevel MINIMAL on a gen-3 non-pro model, so the level a fix
// would use for "off" can be priced today. "" is the current behaviour, which
// sends no thinkingConfig at all and lets Gemini pick.
//
//	TERVA_LIVE_GEMINI_THINKING=1 go test ./packages/provider/ \
//	  -run TestLiveGeminiThinkingLevels -v -count=1
//
// Skipped otherwise, so `just test` never spends.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func TestLiveGeminiThinkingLevels(t *testing.T) {
	if os.Getenv("TERVA_LIVE_GEMINI_THINKING") == "" {
		t.Skip("live probe: set TERVA_LIVE_GEMINI_THINKING=1 (spends real money)")
	}
	model := os.Getenv("TERVA_LIVE_GEMINI_MODEL")
	r, err := build.Resolve(build.Args{
		Provider: "google", Model: model, CWD: testsupport.TempDir(t),
	}, true)
	if err != nil {
		t.Fatalf("resolve a google credential: %v", err)
	}
	cl := r.NewClient()
	t.Logf("model=%s (a rolling alias resolves server-side; modelVersion tells what it became)", r.Model)

	// The same shape the next-step ask uses: a tight cap, one short answer
	// wanted, no tools. That cap is the whole point — a level that thinks 107
	// tokens deep leaves nothing for the sentence.
	const cap200 = 200
	for _, level := range []struct{ name, reasoning string }{
		{"unset (no thinkingConfig — today's behaviour for OFF)", ""},
		{"minimum (thinkingLevel MINIMAL on gen-3 non-pro)", "minimum"},
		{"low (thinkingLevel LOW)", "low"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		stream, streamErr := cl.Stream(ctx, provider.Request{
			Model:  r.Model,
			System: "You are a terse assistant.",
			Messages: []provider.Message{{
				Role: provider.RoleUser,
				Content: []provider.Content{provider.TextBlock{
					Text: "Reply with one short line and nothing else: the smallest next step after a rename broke one call site.",
				}},
				Time: time.Now(),
			}},
			MaxTokens:    cap200,
			Reasoning:    level.reasoning,
			ReasoningSet: true,
		})
		if streamErr != nil {
			cancel()
			t.Errorf("[%s] stream: %v", level.name, streamErr)
			continue
		}
		var sb strings.Builder
		var usage provider.Usage
		var stop provider.StopReason
		for ev := range stream {
			switch e := ev.(type) {
			case provider.EventTextDelta:
				sb.WriteString(e.Delta)
			case provider.EventUsage:
				usage = e.Usage
			case provider.EventDone:
				stop = e.Stop
				if e.Err != nil {
					t.Errorf("[%s] done: %v", level.name, e.Err)
				}
			}
		}
		cancel()
		t.Logf("%-52s thoughts=%-4d out=%-4d stop=%-7s text=%q",
			level.name, usage.ReasoningTokens, usage.OutputTokens, stop, strings.TrimSpace(sb.String()))
	}
}
