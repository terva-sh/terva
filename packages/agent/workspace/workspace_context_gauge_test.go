package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/internal/coretest"
	"terva.sh/terva/packages/agent/modelreg"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/session"
	"terva.sh/terva/packages/testsupport"
)

// The invariant the finding asks for, stated end to end: the window a SESSION
// reports must be the window auto-compaction fires on.
//
// info().ContextWindow rides the wire as the denominator for the web session
// card and app.tsx's ctxTok/ctxWin. It used to be the model's hard ceiling while
// Agent.ContextUsage — which the compaction policy is given — used the effective
// window. On a model with a DesiredContextWindow the two differ by 3.9x, so the
// gauge read 21% at the exact moment the conversation was compacted.
//
// The model is synthetic on purpose: keyed to a catalog row this would become a
// scheduled skip the day that row is retired, and the invariant is about the two
// denominators agreeing, not about any one model.
func TestASessionReportsTheWindowAutoCompactionFiresOn(t *testing.T) {
	const id = "gauge-agreement-model"
	modelreg.SetUserModels([]provider.Model{{
		Provider: "openai-compatible", ID: id,
		ContextWindow: 1050000, DesiredContextWindow: 272000,
	}})
	t.Cleanup(func() { modelreg.SetUserModels(nil) })

	tmp := testsupport.TempDir(t)
	sess, err := session.NewSession(tmp, tmp, "openai-compatible", id, "test")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	s := &wsSession{
		id:       "20260101-120000-aaaaaaaa",
		ws:       &Workspace{ctx: context.Background(), diag: func(string) {}},
		hub:      newWSHub(),
		sess:     sess,
		provider: "openai-compatible",
		model:    id,
		agent:    coretest.NewAgent(nil, id, "", core.Registry{}),
	}

	info := s.info()
	_, window := s.agent.ContextUsage()

	if window == 0 {
		t.Fatal("the agent resolved no window for the synthetic model; this test is not exercising the invariant")
	}
	if info.ContextWindow != window {
		t.Errorf("SessionInfo.ContextWindow = %d but auto-compaction divides by %d — every gauge fed by "+
			"this field reads a different fullness from the one that triggers compaction",
			info.ContextWindow, window)
	}
	// And it is the effective window specifically, not the ceiling. Without this
	// the test would also pass if BOTH sides regressed to the hard ceiling.
	if info.ContextWindow != 272000 {
		t.Errorf("SessionInfo.ContextWindow = %d, want the effective window 272000 (hard ceiling is 1050000)",
			info.ContextWindow)
	}
	var _ ctrlproto.SessionInfo = info
}

// Every session gauge reads the session provider's row, the row the engine
// reads. Two providers list one id here, and the anthropic row sorts first. The
// context breakdown and the usage pane asked for the bare id, so they showed
// the anthropic row's 1M while the status bar and auto-compaction used cpa's
// 500k.
func TestEverySessionGaugeReadsTheSessionProvidersRow(t *testing.T) {
	const id = "gauge-shared-id"
	modelreg.SetUserModels([]provider.Model{
		{Provider: "anthropic", ID: id, ContextWindow: 1_000_000},
		{Provider: "cpa", ID: id, ContextWindow: 1_000_000, DesiredContextWindow: 500_000},
	})
	t.Cleanup(func() { modelreg.SetUserModels(nil) })

	tmp := testsupport.TempDir(t)
	sess, err := session.NewSession(tmp, tmp, "cpa", id, "test")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	s := &wsSession{
		id:       "20260101-120000-bbbbbbbb",
		ws:       &Workspace{ctx: context.Background(), diag: func(string) {}},
		hub:      newWSHub(),
		sess:     sess,
		provider: "cpa",
		model:    id,
		agent:    coretest.NewAgent(nil, id, "", core.Registry{}, core.WithProvider("cpa")),
	}

	_, window := s.agent.ContextUsage()
	if window != 500_000 {
		t.Fatalf("the engine window is %d, want cpa's 500000; the fixture is not exercising the shared id", window)
	}
	for name, got := range map[string]int{
		"SessionInfo.ContextWindow": s.info().ContextWindow,
		"UsageView.Window":          s.usageView().Window,
		"ContextBreakdown.Window":   s.contextBreakdown().Window,
	} {
		if got != window {
			t.Errorf("%s = %d, but auto-compaction divides by %d", name, got, window)
		}
	}
}
