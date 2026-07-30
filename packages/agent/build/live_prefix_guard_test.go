package build_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// The live prefix-change probe: the two questions no unit test can answer,
// because both are about what a real provider does with the bytes we send it.
//
//  1. Does the cache-aware compaction actually HIT the cache? Its failure is
//     silent — a prefix match that missed produces the same summary and the same
//     transcript and raises no error, and differs only in being billed at full
//     price behind a label promising otherwise.
//  2. Does the guard fire on a real model switch, with the provider's own
//     cached-token counts driving its threshold, and is the compaction it
//     triggers billed against the OUTGOING model?
//
// Skipped unless TERVA_LIVE_GUARD_PROBE is set, because it SPENDS REAL MONEY —
// roughly $0.35 on Anthropic at the default payload, most of it the one-time
// cache write. Everything is the shipped path except the Asker, which stands in
// for the TUI/web question dialog.
//
//	TERVA_HOME=/tmp/scratch-home \
//	TERVA_LIVE_GUARD_PROBE=1 \
//	TERVA_LIVE_GUARD_PAYLOAD=/tmp/200kb-of-source.txt \
//	TERVA_LIVE_GUARD_SWAP_TO=claude-opus-4-6 \
//	  go test ./packages/agent/build -run TestLivePrefixGuard -v
//
// The scratch TERVA_HOME needs auth.json plus a config.json selecting the
// provider/model, `"auto_compact": "off"`, and both engine features on. The
// payload must be big enough that the cached prefix clears prefixChangeMinTokens
// (50k) — about 200 KB of source.
func TestLivePrefixGuard(t *testing.T) {
	if os.Getenv("TERVA_LIVE_GUARD_PROBE") == "" {
		t.Skip("live probe: set TERVA_LIVE_GUARD_PROBE=1 (spends real money)")
	}
	payloadPath := os.Getenv("TERVA_LIVE_GUARD_PAYLOAD")
	swapTo := os.Getenv("TERVA_LIVE_GUARD_SWAP_TO")
	if payloadPath == "" || swapTo == "" {
		t.Fatal("set TERVA_LIVE_GUARD_PAYLOAD and TERVA_LIVE_GUARD_SWAP_TO")
	}
	bulk, err := os.ReadFile(payloadPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	args, err := build.ParseArgs([]string{"--no-ext", "--no-mcp"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := build.Resolve(args, true)
	if err != nil {
		t.Fatal(err)
	}

	var asked []core.UserQuestion
	// SetAsker must precede NewAgent: that is where the agent picks it up.
	r.SetAsker(askerFunc(func(_ context.Context, qs []core.UserQuestion) ([]core.UserAnswer, error) {
		asked = append(asked, qs...)
		return []core.UserAnswer{{Answer: qs[0].Options[0]}}, nil // "Compact first"
	}))
	ag := r.NewAgent()

	sessPath := filepath.Join(testsupport.TempDir(t), "probe.jsonl")
	sess, err := core.NewSessionAtPath(sessPath, r.CWD, r.Provider, r.Model, "probe")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	build.WireHeadlessSessionPersist(ag, sess)

	var compaction core.CompactResult
	ag.AddTranscriptCompactedObserver(func(_ []provider.Message, res core.CompactResult) {
		compaction = res
	})

	turn := func(label, prompt string) provider.Usage {
		if err := ag.PromptWithPolicy(ctx, prompt, nil, func(core.AgentEvent) {}); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		u := ag.LastTurnUsage()
		t.Logf("%-24s input=%-6d cache_read=%-7d cache_write=%-7d $%.4f",
			label, u.InputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.CostUSD)
		return u
	}

	// Three turns, not two. The first writes the cache; the rest read it back.
	// Two would be exactly AutoCompactKeepTail (4) messages, and the guard would
	// correctly decline to offer a compaction with nothing to summarize — which is
	// a real behavior, and was the first thing this probe caught about itself.
	turn("turn 1 (cache write)", "Here is some code. Reply with only the word ACK.\n\n"+string(bulk))
	turn("turn 2 (cache read)", "Reply with only the word TWO.")
	warm := turn("turn 3 (cache read)", "Reply with only the word THREE.")

	if warm.CacheReadTokens < 50_000 {
		t.Fatalf("cached prefix is %d tokens; the payload is too small to reach the guard's threshold",
			warm.CacheReadTokens)
	}

	outgoing := r.Model
	ag.SetModel(swapTo)
	turn("turn 4 (post-guard)", "Reply with only the word FOUR.")

	// The guard fired, once, and said what it would cost.
	if len(asked) != 1 {
		t.Fatalf("the guard asked %d questions; want exactly 1", len(asked))
	}
	t.Logf("the guard asked: %s", asked[0].Question)

	// The compaction summarized against the model that still had the transcript
	// cached — and actually hit that cache.
	if compaction.Strategy != core.CompactWarm {
		t.Errorf("compaction strategy = %q (%s); want %q",
			compaction.Strategy, compaction.FallbackReason, core.CompactWarm)
	}
	u := compaction.Usage
	hit := float64(u.CacheReadTokens) / float64(u.CacheReadTokens+u.InputTokens)
	t.Logf("compaction: strategy=%s cache_read=%d input=%d hit=%.1f%% $%.4f (on %s, the outgoing model)",
		compaction.Strategy, u.CacheReadTokens, u.InputTokens, hit*100, u.CostUSD, outgoing)
	if hit < 0.8 {
		t.Errorf("compaction cache-hit fraction = %.2f; want >= 0.80 — the warm prefix MISSED, so it "+
			"was billed at full price while reporting itself cache-aware", hit)
	}
	fmt.Printf("\nsummary produced:\n%s\n", compaction.Summary)
}

type askerFunc func(context.Context, []core.UserQuestion) ([]core.UserAnswer, error)

func (f askerFunc) Ask(ctx context.Context, qs []core.UserQuestion) ([]core.UserAnswer, error) {
	return f(ctx, qs)
}
