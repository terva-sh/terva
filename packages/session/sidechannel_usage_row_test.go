package session

import (
	"context"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/transcripttest"
	"terva.sh/terva/packages/provider"
)

// The side-channel row is the delegated row's twin, and these tests mirror
// delegated_usage_row_test.go on purpose: the two mark the same defect from
// opposite sides. Delegated is spend a sub-agent made on this session's
// behalf; a side-channel row is spend the HOST made on this session's
// credentials, and until the mark existed the only way to find one in a
// session file was its shape, a usage row with no message after it.

// TestRecordSideChannelUsageFiresTheMarkedObserverNotThePlainOne is the
// write-side fix: a host persisting on the plain observer used to receive an
// idle suggestion's spend and write it as an ordinary turn.
func TestRecordSideChannelUsageFiresTheMarkedObserverNotThePlainOne(t *testing.T) {
	a := &core.Agent{}
	rec := &transcripttest.Recorder{}
	a.AttachTranscriptStore(rec)

	a.RecordSideChannelUsage("next_step", provider.Usage{InputTokens: 12, CacheReadTokens: 90000, OutputTokens: 20, CostUSD: 0.05})

	plain := len(rec.Usage(core.UsageTurn))
	side := rec.Usage(core.UsageSideChannel)
	if len(side) != 1 {
		t.Fatalf("%d side-channel records; want 1", len(side))
	}
	gotSource, gotUsage := side[0].Source, side[0].Usage
	if plain != 0 {
		t.Errorf("plain usage observer fired %d time(s) for side-channel spend; a host persists on it, so the row lands unmarked", plain)
	}
	if gotSource != "next_step" {
		t.Errorf("source = %q; want next_step", gotSource)
	}
	if gotUsage.OutputTokens != 20 {
		t.Errorf("marked observer got %+v; want the call's own usage", gotUsage)
	}
}

// TestSideChannelSpendStaysInTheTotalAndIsSeparatelyReadable: the mark is
// attribution, not exclusion. The money is in the headline and readable apart.
func TestSideChannelSpendStaysInTheTotalAndIsSeparatelyReadable(t *testing.T) {
	a := &core.Agent{}
	a.RecordSideChannelUsage("next_step", provider.Usage{InputTokens: 1000, CostUSD: 0.20})
	if got := a.Cost().CostUSD; got != 0.20 {
		t.Errorf("Cost() = %.2f; want 0.20, side-channel spend is a subset of the total", got)
	}
	if got := a.SideChannelCost().CostUSD; got != 0.20 {
		t.Errorf("SideChannelCost() = %.2f; want 0.20", got)
	}
	if got := a.LastTurnUsage(); got != (provider.Usage{}) {
		t.Errorf("LastTurnUsage() = %+v after a side-channel call; the context gauge must not move", got)
	}
}

// TestZeroSideChannelUsageIsIgnored: a stream that failed before its usage
// event hands back a zero Usage, and every caller books unconditionally.
func TestZeroSideChannelUsageIsIgnored(t *testing.T) {
	a := &core.Agent{}
	rec := &transcripttest.Recorder{}
	a.AttachTranscriptStore(rec)
	a.RecordSideChannelUsage("next_step", provider.Usage{})
	if fired := len(rec.Usage(core.UsageSideChannel)); fired != 0 || a.Cost() != (provider.Usage{}) {
		t.Errorf("a zero usage fired %d observer(s) and left Cost() = %+v; want nothing", fired, a.Cost())
	}
}

// TestSideChannelRowNeverBecomesTheResumedContextGauge is the read-side fix. In
// memory a side-channel call never touched the snapshot, but on disk it was an
// ordinary row, so a session whose last row was a side chat's bespoke prompt
// resumed believing its transcript was that size.
func TestSideChannelRowNeverBecomesTheResumedContextGauge(t *testing.T) {
	sess, path := seededSession(t, "s.jsonl")
	own := provider.Usage{InputTokens: 2000, CacheReadTokens: 40000, OutputTokens: 100, CostUSD: 0.05}
	cum := own
	if err := sess.AppendUsage(own, cum); err != nil {
		t.Fatalf("AppendUsage: %v", err)
	}
	side := provider.Usage{InputTokens: 700, OutputTokens: 30, CostUSD: 0.01}
	cum = cum.Add(side)
	if err := sess.AppendSideChannelUsage("side_chat", side, cum); err != nil {
		t.Fatalf("AppendSideChannelUsage: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	total, lastTurn, _, err := SessionUsageDetail(path)
	if err != nil {
		t.Fatalf("SessionUsageDetail: %v", err)
	}
	if lastTurn.InputTokens != own.InputTokens {
		t.Errorf("lastTurn.InputTokens = %d; want %d, the side call's prompt became this session's context gauge",
			lastTurn.InputTokens, own.InputTokens)
	}
	if got := total.CostUSD; got < 0.059 || got > 0.061 {
		t.Errorf("cumulative cost = %.4f; want ~0.06 (own 0.05 + side 0.01)", got)
	}
}

// TestReplayRowsCarryTheSideChannelSource is what lets session_inspect answer
// what each surface cost, through both decoders.
func TestReplayRowsCarryTheSideChannelSource(t *testing.T) {
	sess, path := seededSession(t, "s.jsonl")
	if err := sess.AppendUsage(provider.Usage{InputTokens: 10}, provider.Usage{InputTokens: 10}); err != nil {
		t.Fatalf("AppendUsage: %v", err)
	}
	if err := sess.AppendSideChannelUsage("next_step", provider.Usage{InputTokens: 5}, provider.Usage{InputTokens: 15}); err != nil {
		t.Fatalf("AppendSideChannelUsage: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	count := func(rows []ReplayRow) (own, side int, source string) {
		for _, r := range rows {
			if r.Kind != ReplayRowUsage {
				continue
			}
			if r.Source != "" {
				side++
				source = r.Source
			} else {
				own++
			}
		}
		return
	}

	rows, _, err := ReadReplayRows(path)
	if err != nil {
		t.Fatalf("ReadReplayRows: %v", err)
	}
	if own, side, src := count(rows); own != 1 || side != 1 || src != "next_step" {
		t.Errorf("ReadReplayRows: %d own / %d side (%q); want 1 / 1 (next_step)", own, side, src)
	}

	// The streaming decoder is the one session_inspect reads through, and it
	// once omitted a field its twin decoded (see the Delegated comment there).
	var streamed []ReplayRow
	if _, _, err := StreamReplayRows(context.Background(), path, 0, func(_ int, r ReplayRow) { streamed = append(streamed, r) }); err != nil {
		t.Fatalf("StreamReplayRows: %v", err)
	}
	if own, side, src := count(streamed); own != 1 || side != 1 || src != "next_step" {
		t.Errorf("StreamReplayRows: %d own / %d side (%q); want 1 / 1 (next_step)", own, side, src)
	}
}
