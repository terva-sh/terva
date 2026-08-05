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

	cumulative, lastTurn, _, err := SessionUsageDetail(path)
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

	cumulative, lastTurn, resume, err := SessionUsageDetail(path)
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

	// ...and the gauge must not seed from lastTurn EITHER, which is the half
	// that used to be missing. turn1 described a transcript the compaction
	// replaced, so resuming on it reports the size of something that is no
	// longer there — 1000 tokens against a one-line checkpoint here, 98k
	// against a ~5.8k checkpoint on the real session that surfaced this.
	if resume.InputTokens >= turn1.InputTokens {
		t.Errorf("resumeContext.InputTokens = %d; want well under turn1's %d — the gauge resumed on a transcript that no longer exists",
			resume.InputTokens, turn1.InputTokens)
	}
	if resume.InputTokens == 0 {
		t.Error("resumeContext is 0 on a non-empty checkpoint — the summary has a size and the gauge should show it")
	}
}

// The resume baseline must equal what the LIVE re-baseline produced, or the
// gauge jumps at the moment you reopen the session: compact.go seeds
// estimateTokens(next) in memory, and this recovers the same number from the
// file. Asserted against estimateTokens itself rather than a literal, so the
// two cannot drift apart if the heuristic is ever retuned.
func TestAResumedGaugeAgreesWithTheLiveOne(t *testing.T) {
	s, path := newUsageSession(t)

	if err := s.AppendUsage(provider.Usage{InputTokens: 50_000}, provider.Usage{InputTokens: 50_000}); err != nil {
		t.Fatal(err)
	}
	kept := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "## Context Summary\nwe decided on the tri-state"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "acknowledged"}}},
	}
	if err := s.AppendCompaction(kept, CompactResult{Usage: provider.Usage{InputTokens: 40_000}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	_, _, resume, err := SessionUsageDetail(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := estimateTokens(kept); resume.InputTokens != want {
		t.Errorf("resumed gauge = %d, live re-baseline = %d — reopening the session would move the gauge on its own",
			resume.InputTokens, want)
	}
}

// /clear writes AppendCompaction(nil): the transcript really is empty, and the
// gauge must say so. This is why the trailing compaction is tracked by a FLAG
// and not by "the estimate is non-zero" — 0 here is the right answer, and a
// bare int cannot tell it apart from "no compaction happened".
func TestClearResumesAtAnEmptyGauge(t *testing.T) {
	s, path := newUsageSession(t)

	if err := s.AppendUsage(provider.Usage{InputTokens: 80_000}, provider.Usage{InputTokens: 80_000}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendCompaction(nil, CompactResult{}); err != nil {
		t.Fatal(err)
	}
	// Deliberately NOT closed. Close prunes a session whose messagesAppended is
	// 0, and AppendCompaction(nil) sets exactly that — the /clear shape. Every
	// line is flushed at write time (writeLineLocked), so the file on disk is
	// already complete; closing here would delete the case under test.

	_, lastTurn, resume, err := SessionUsageDetail(path)
	if err != nil {
		t.Fatal(err)
	}
	if resume.InputTokens != 0 {
		t.Errorf("gauge resumes at %d after /clear; want 0 — the transcript is empty", resume.InputTokens)
	}
	if lastTurn.InputTokens != 80_000 {
		t.Errorf("lastTurn = %d; want 80000 — what the turn spent is history and /clear does not rewrite it", lastTurn.InputTokens)
	}
}

// A real turn after the compaction hands the gauge back to provider-reported
// numbers. The estimate is a stand-in for a measurement that has not happened
// yet, not a preference — same handoff as in memory, where the next completed
// request overwrites what SetLastTurn seeded.
func TestATurnAfterTheCompactionTakesTheGaugeBack(t *testing.T) {
	s, path := newUsageSession(t)

	turn1 := provider.Usage{InputTokens: 50_000, CostUSD: 0.1}
	if err := s.AppendUsage(turn1, turn1); err != nil {
		t.Fatal(err)
	}
	compact := provider.Usage{InputTokens: 40_000, CostUSD: 0.2}
	if err := s.AppendCompaction([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "## Context Summary"}}},
	}, CompactResult{Usage: compact}); err != nil {
		t.Fatal(err)
	}
	turn2 := provider.Usage{InputTokens: 6_000, CacheReadTokens: 1_000, CostUSD: 0.02}
	if err := s.AppendUsage(turn2, turn1.Add(compact).Add(turn2)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	_, lastTurn, resume, err := SessionUsageDetail(path)
	if err != nil {
		t.Fatal(err)
	}
	if resume != lastTurn {
		t.Errorf("resumeContext %+v != lastTurn %+v — a measured turn should win over the estimate it replaced", resume, lastTurn)
	}
	if resume.InputTokens != turn2.InputTokens {
		t.Errorf("resumeContext.InputTokens = %d; want %d", resume.InputTokens, turn2.InputTokens)
	}
}

// With no compaction anywhere the two are the same value, so nothing that reads
// resumeContext changes behaviour on an ordinary session.
func TestWithoutACompactionTheTwoAgree(t *testing.T) {
	s, path := newUsageSession(t)

	turn1 := provider.Usage{InputTokens: 1_000, CostUSD: 0.01}
	if err := s.AppendUsage(turn1, turn1); err != nil {
		t.Fatal(err)
	}
	turn2 := provider.Usage{InputTokens: 2_000, CacheReadTokens: 500, CostUSD: 0.02}
	if err := s.AppendUsage(turn2, turn1.Add(turn2)); err != nil {
		t.Fatal(err)
	}
	// A message so Close keeps the file: it discards a fresh session that
	// appended none, and a session with usage rows and no messages is a shape
	// only a test makes.
	if err := s.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	_, lastTurn, resume, err := SessionUsageDetail(path)
	if err != nil {
		t.Fatal(err)
	}
	if resume != lastTurn {
		t.Errorf("resumeContext %+v != lastTurn %+v on a session with no compaction", resume, lastTurn)
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

	cumulative, lastTurn, _, err := SessionUsageDetail(path)
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

// A server-side compaction's row has to say how big the thing it replaced was,
// because nothing else on it can.
//
// The client strategies leave a prose summary in the row's own messages: read it
// back and you know roughly what was there. This one leaves an encrypted blob
// terva cannot decrypt, so the row is otherwise an opaque item of unknown
// provenance replacing an unknown amount of history. The count plus the
// append-only file — the superseded turns are still above this row — is what
// keeps such a session auditable without paying a summarizer to describe, for a
// human, a transcript the model already has.
func TestServerCompactedRowSaysWhatItReplaced(t *testing.T) {
	s, path := newUsageSession(t)

	if err := s.AppendCompaction(
		[]provider.Message{{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.CompactionBlock{ID: "cmp_1", Encrypted: "gAAAAABopaque==", Provider: "openai-codex"}}}},
		CompactResult{
			Usage:              provider.Usage{InputTokens: 12_000, OutputTokens: 800, CostUSD: 0.02},
			Strategy:           CompactProvider,
			SupersededMessages: 47,
		},
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
	for _, want := range []string{
		`"strategy":"provider"`,
		`"superseded":47`,
		// Provenance rides the block itself: a blob whose issuing provider is
		// unknown cannot be told apart from one this provider can replay, and
		// guessing is the guess that loses the conversation.
		`"provider":"openai-codex"`,
		`"encrypted_content":"gAAAAABopaque=="`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the compaction row is missing %s:\n%s", want, raw)
		}
	}

	// Omitted where it says nothing, like every other optional key on the row: a
	// zero would have to be read as "replaced nothing", which no compaction did.
	s2, path2 := newUsageSession(t)
	if err := s2.AppendCompaction(
		[]provider.Message{{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "## Context Summary"}}}},
		CompactResult{Strategy: CompactWarm},
	); err != nil {
		t.Fatal(err)
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
	raw2, err := os.ReadFile(path2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw2), `"superseded"`) {
		t.Errorf("an unset count was written anyway:\n%s", raw2)
	}
}
