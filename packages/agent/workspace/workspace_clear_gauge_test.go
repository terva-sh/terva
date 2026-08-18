package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// clearGaugeSession is a minimal session whose clear() can run: a real
// transcript file (clear writes a compaction floor marker) and a hub to
// broadcast into.
func clearGaugeSession(t *testing.T) *wsSession {
	t.Helper()
	tmp := testsupport.TempDir(t)
	sess, err := core.NewSession(tmp, tmp, "p", "claude-sonnet-4-5", "test")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return &wsSession{
		id:    "cleared",
		ws:    &Workspace{ctx: context.Background(), diag: func(string) {}},
		hub:   newWSHub(),
		sess:  sess,
		agent: core.NewAgent(nil, "claude-sonnet-4-5", "", core.Registry{}),
	}
}

// A clear empties the transcript, and the context gauge has to follow it down.
//
// SetMessages does not touch the cost tracker, and clear had no equivalent of
// the re-baseline compaction gets for free (installCompaction seeds the
// post-compaction estimate). So LastTurnUsage went on describing the transcript
// that had just been thrown away, and every reader derived from it --
// info().ContextTokens, the status-bar gauge, the usage pane -- reported a full
// context for an empty conversation until the next turn landed usage.
//
// This is the same family as the post-compaction zero, failing the other way:
// there the client under-reported a context that existed, here the daemon
// over-reported one that did not.
func TestClearReBaselinesTheContextGauge(t *testing.T) {
	s := clearGaugeSession(t)
	s.agent.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "a long conversation"}}},
	})
	s.agent.SeedLastTurnUsage(provider.Usage{InputTokens: 140_000, CacheReadTokens: 12_000})

	if got := s.agent.LastTurnUsage().InputTokens; got != 140_000 {
		t.Fatalf("fixture: seeded gauge = %d, want 140000", got)
	}

	if err := s.clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}

	if got := s.agent.LastTurnUsage(); got != (provider.Usage{}) {
		t.Errorf("after clear LastTurnUsage = %+v, want zero -- the emptied transcript still reports its old prompt size", got)
	}
}

// The snapshot clear broadcasts is what every client re-seeds its gauge from,
// so the re-baseline has to be visible THERE, not merely in the tracker. This
// is the assertion a client actually depends on.
func TestAClearedSessionReportsAnEmptyContext(t *testing.T) {
	s := clearGaugeSession(t)
	s.agent.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "history"}}},
	})
	s.agent.SeedLastTurnUsage(provider.Usage{InputTokens: 88_000})

	if got := s.info().ContextTokens; got != 88_000 {
		t.Fatalf("fixture: info().ContextTokens = %d, want 88000", got)
	}

	if err := s.clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}

	if got := s.info().ContextTokens; got != 0 {
		t.Errorf("info().ContextTokens = %d after a clear, want 0 -- clients seed the gauge from this", got)
	}
}
