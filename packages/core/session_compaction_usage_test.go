package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// The invariant these tests pin: a compaction's spend is COST, never CONTEXT.
//
// The summarizer reads the whole transcript, so its input count is
// transcript-sized by construction. In memory three guards keep that number
// away from the context gauge (CostTracker.AddTotalOnly, SetLastTurn's
// re-baseline, and the fact that compaction never fires the usage observer).
// None of them survive the file: on resume the gauge is rebuilt from the JSONL
// alone. So the ledger has to carry the distinction itself — which is why the
// spend rides the "compaction" row and why SessionUsageDetail subtracts it out
// of the last-turn delta.
//
// Get this wrong and the failure is not cosmetic: the resumed gauge reads
// roughly double, ShouldAutoCompact fires on the next check, and the user
// watches terva condense a transcript it condensed thirty seconds ago.

func newUsageSession(t *testing.T) (*Session, string) {
	t.Helper()
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	s, err := NewSessionAtPath(path, "/ws", "p", "m", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

// A compaction between the final two turns must not inflate the last-turn
// snapshot. cum_N = cum_{N-1} + compaction + u_N, so the naive delta hands the
// gauge the compaction's transcript-sized read on top of the real turn.
func TestSessionUsageDetailSubtractsCompactionBetweenTurns(t *testing.T) {
	s, path := newUsageSession(t)

	turn1 := provider.Usage{InputTokens: 1_000, OutputTokens: 100, CacheReadTokens: 200, CostUSD: 0.01}
	if err := s.AppendUsage(turn1, turn1); err != nil {
		t.Fatal(err)
	}

	// The auto-compact: a transcript-sized read, an order of magnitude larger
	// than any single turn. This is exactly the shape that poisons the gauge.
	compact := provider.Usage{InputTokens: 90_000, OutputTokens: 900, CacheReadTokens: 0, CostUSD: 0.45}
	if err := s.AppendCompaction([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "## Context Summary"}}},
	}, CompactResult{Usage: compact}); err != nil {
		t.Fatal(err)
	}

	turn2 := provider.Usage{InputTokens: 300, OutputTokens: 50, CacheReadTokens: 120, CostUSD: 0.005}
	cum2 := turn1.Add(compact).Add(turn2)
	if err := s.AppendUsage(turn2, cum2); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	cumulative, lastTurn, err := SessionUsageDetail(path)
	if err != nil {
		t.Fatal(err)
	}

	// The gauge sees the turn, not the condense.
	if lastTurn.InputTokens != turn2.InputTokens {
		t.Errorf("lastTurn.InputTokens = %d; want %d (the compaction's %d must not land in the gauge)",
			lastTurn.InputTokens, turn2.InputTokens, compact.InputTokens)
	}
	if lastTurn.OutputTokens != turn2.OutputTokens {
		t.Errorf("lastTurn.OutputTokens = %d; want %d", lastTurn.OutputTokens, turn2.OutputTokens)
	}
	if lastTurn.CacheReadTokens != turn2.CacheReadTokens {
		t.Errorf("lastTurn.CacheReadTokens = %d; want %d", lastTurn.CacheReadTokens, turn2.CacheReadTokens)
	}

	// But the money is all still there.
	if cumulative.InputTokens != cum2.InputTokens {
		t.Errorf("cumulative.InputTokens = %d; want %d (compaction spend is real)",
			cumulative.InputTokens, cum2.InputTokens)
	}
}

// A compaction AFTER the newest turn is in no cumulative row at all: the
// in-memory total has it, but the next turn's row — which is where it would
// have been written — never happened. Compact and then quit for the day, an
// entirely ordinary thing to do, and the spend simply vanished from the ledger.
func TestSessionUsageDetailFoldsTrailingCompactionIntoCumulative(t *testing.T) {
	s, path := newUsageSession(t)

	turn1 := provider.Usage{InputTokens: 1_000, OutputTokens: 100, CostUSD: 0.01}
	if err := s.AppendUsage(turn1, turn1); err != nil {
		t.Fatal(err)
	}
	compact := provider.Usage{InputTokens: 90_000, OutputTokens: 900, CostUSD: 0.45}
	if err := s.AppendCompaction([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "## Context Summary"}}},
	}, CompactResult{Usage: compact}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	cumulative, lastTurn, err := SessionUsageDetail(path)
	if err != nil {
		t.Fatal(err)
	}

	want := turn1.Add(compact)
	if cumulative.InputTokens != want.InputTokens {
		t.Errorf("cumulative.InputTokens = %d; want %d — the trailing compaction's spend was dropped",
			cumulative.InputTokens, want.InputTokens)
	}
	if cumulative.CostUSD != want.CostUSD {
		t.Errorf("cumulative.CostUSD = %v; want %v", cumulative.CostUSD, want.CostUSD)
	}

	// ...and it still must not reach the gauge. This is the trap: folding the
	// trailing compaction into the total is right, folding it into lastTurn
	// would re-arm every threshold check on a transcript that was just
	// condensed — the very thing the compaction did.
	if lastTurn.InputTokens != turn1.InputTokens {
		t.Errorf("lastTurn.InputTokens = %d; want %d (trailing compaction must not seed the gauge)",
			lastTurn.InputTokens, turn1.InputTokens)
	}
}

// The compaction row's on-disk shape IS the A/B's dataset — the protocol in
// docs/plans/cache-aware-compaction-ab.md reads these exact keys with jq. Pin
// them, so a rename doesn't quietly turn every analysis command into a
// zero-row result that reads like a clean run.
func TestCompactionRowRecordsTheStrategyOnDisk(t *testing.T) {
	s, path := newUsageSession(t)

	// A warm compaction that HIT the cache: the transcript was re-read at cache
	// rates. This is the row the experiment is trying to count.
	warm := provider.Usage{InputTokens: 400, CacheReadTokens: 89_600, OutputTokens: 900, CostUSD: 0.05}
	if err := s.AppendCompaction(
		[]provider.Message{{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "## Context Summary"}}}},
		CompactResult{Usage: warm, Strategy: CompactWarm},
	); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"type":"compaction"`, `"strategy":"warm"`, `"cache_read_tokens":89600`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the compaction row is missing %s — the A/B's jq queries read this key:\n%s", want, raw)
		}
	}
	// "cold" is the default and stays implicit, so a legacy row (no strategy)
	// and a cold row are the same thing. Nothing to migrate.
	if strings.Contains(string(raw), `"fallback_reason"`) {
		t.Error("a clean warm run wrote a fallback_reason; it must be omitted, or the fallback rate reads high")
	}
}

// A session written before compaction rows carried usage must read back exactly
// as it does today: both corrections are zero and nothing shifts.
func TestSessionUsageDetailUnchangedForLegacyCompactionRows(t *testing.T) {
	s, path := newUsageSession(t)

	turn1 := provider.Usage{InputTokens: 1_000, OutputTokens: 100, CostUSD: 0.01}
	if err := s.AppendUsage(turn1, turn1); err != nil {
		t.Fatal(err)
	}
	// Zero usage is what an old row decodes to (the field is absent).
	if err := s.AppendCompaction([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "## Context Summary"}}},
	}, CompactResult{}); err != nil {
		t.Fatal(err)
	}
	turn2 := provider.Usage{InputTokens: 300, OutputTokens: 50, CostUSD: 0.005}
	cum2 := turn1.Add(turn2)
	if err := s.AppendUsage(turn2, cum2); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	cumulative, lastTurn, err := SessionUsageDetail(path)
	if err != nil {
		t.Fatal(err)
	}
	if lastTurn.InputTokens != turn2.InputTokens {
		t.Errorf("lastTurn.InputTokens = %d; want %d", lastTurn.InputTokens, turn2.InputTokens)
	}
	if cumulative.InputTokens != cum2.InputTokens {
		t.Errorf("cumulative.InputTokens = %d; want %d", cumulative.InputTokens, cum2.InputTokens)
	}
}
