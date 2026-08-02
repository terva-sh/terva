package core

import (
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func seededSession(t *testing.T, name string) (*Session, string) {
	t.Helper()
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, name)
	s, err := NewSessionAtPath(path, dir, "openai-codex", "gpt-5.6-sol", "test")
	if err != nil {
		t.Fatalf("NewSessionAtPath: %v", err)
	}
	if err := s.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "hi"}},
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	return s, path
}

// TestRecordDelegatedUsageDoesNotFireThePlainUsageObserver is the write-side
// fix: a host persisting on the usage observer used to receive a sub-agent's
// spend and write it as an ordinary row, indistinguishable from this session's
// own request.
func TestRecordDelegatedUsageDoesNotFireThePlainUsageObserver(t *testing.T) {
	a := &Agent{}
	var plain, delegated int
	a.AddUsageObserver(func(u, cum provider.Usage) { plain++ })
	a.AddDelegatedUsageObserver(func(u, cum provider.Usage) { delegated++ })

	a.RecordDelegatedUsage(provider.Usage{InputTokens: 90000, OutputTokens: 300, CostUSD: 0.45})

	if plain != 0 {
		t.Errorf("plain usage observer fired %d time(s) for delegated spend; a host persists "+
			"on it, so the row lands unmarked", plain)
	}
	if delegated != 1 {
		t.Errorf("delegated observer fired %d time(s); want 1", delegated)
	}
}

// TestDelegatedSpendStaysInTheCumulativeTotal pins what must NOT change: the
// separation is about attribution, not about hiding the money.
func TestDelegatedSpendStaysInTheCumulativeTotal(t *testing.T) {
	a := &Agent{}
	a.RecordDelegatedUsage(provider.Usage{InputTokens: 1000, CostUSD: 2.50})
	if got := a.Cost().CostUSD; got != 2.50 {
		t.Errorf("Cost() = %.2f; want 2.50 — delegated spend is a subset of the total, not excluded from it", got)
	}
	if got := a.DelegatedCost().CostUSD; got != 2.50 {
		t.Errorf("DelegatedCost() = %.2f; want 2.50", got)
	}
}

// TestDelegatedRowNeverBecomesTheResumedContextGauge is the read-side fix. A
// child is routinely larger than its parent, so a session whose final row was a
// child's would resume believing its own transcript was that big — and the
// first threshold check would auto-compact a transcript that never grew.
func TestDelegatedRowNeverBecomesTheResumedContextGauge(t *testing.T) {
	sess, path := seededSession(t, "s.jsonl")
	own := provider.Usage{InputTokens: 2000, CacheReadTokens: 40000, OutputTokens: 100, CostUSD: 0.05}
	cum := own
	if err := sess.AppendUsage(own, cum); err != nil {
		t.Fatalf("AppendUsage: %v", err)
	}
	child := provider.Usage{InputTokens: 250000, OutputTokens: 900, CostUSD: 1.30}
	cum = cum.Add(child)
	if err := sess.AppendDelegatedUsage(child, cum); err != nil {
		t.Fatalf("AppendDelegatedUsage: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	total, lastTurn, _, err := SessionUsageDetail(path)
	if err != nil {
		t.Fatalf("SessionUsageDetail: %v", err)
	}
	if lastTurn.InputTokens != own.InputTokens {
		t.Errorf("lastTurn.InputTokens = %d; want %d — the child's prompt became this session's context gauge",
			lastTurn.InputTokens, own.InputTokens)
	}
	// The money is still all there.
	if got := total.CostUSD; got < 1.34 || got > 1.36 {
		t.Errorf("cumulative cost = %.4f; want ~1.35 (own 0.05 + delegated 1.30)", got)
	}
}

// TestReplayRowsMarkDelegatedUsage is what lets session_inspect (and any other
// reader) keep a sub-agent's cold prompt out of the parent's cache-hit rate.
func TestReplayRowsMarkDelegatedUsage(t *testing.T) {
	sess, path := seededSession(t, "s.jsonl")
	if err := sess.AppendUsage(provider.Usage{InputTokens: 10}, provider.Usage{InputTokens: 10}); err != nil {
		t.Fatalf("AppendUsage: %v", err)
	}
	if err := sess.AppendDelegatedUsage(provider.Usage{InputTokens: 900}, provider.Usage{InputTokens: 910}); err != nil {
		t.Fatalf("AppendDelegatedUsage: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	rows, _, err := ReadReplayRows(path)
	if err != nil {
		t.Fatalf("ReadReplayRows: %v", err)
	}
	var own, del int
	for _, r := range rows {
		if r.Kind != ReplayRowUsage {
			continue
		}
		if r.Delegated {
			del++
		} else {
			own++
		}
	}
	if own != 1 || del != 1 {
		t.Errorf("replay rows: %d own / %d delegated; want 1 / 1", own, del)
	}
}
