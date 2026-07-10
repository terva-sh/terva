package workspace

import (
	"context"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// TestWebExtHooksRefreshTriggersRebuild verifies the daemon ext hooks route the
// extension driver's refresh_context (protocol 3) and set_withdrawn_tools
// (protocol 4) events into a session rebuild — rather than the inherited
// no-ops, which left a dynamic extension's context/tools invisible to the model
// on both the carrier TUI and terva web.
func TestWebExtHooksRefreshTriggersRebuild(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	args := build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true}
	w, err := NewWorkspace(args, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	defer w.Close()

	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	s := w.existing(info.ID)
	if s == nil {
		t.Fatal("session not materialized")
	}

	for _, tc := range []struct {
		name   string
		call   func()
		reason string
	}{
		{"RefreshContext", func() { webExtHooks{s: s}.RefreshContext() }, "extension-context"},
		{"RefreshTools", func() { webExtHooks{s: s}.RefreshTools() }, "tool-withdrawal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Seed a completed turn so the extension-driven rebuild announces
			// itself: pre-first-turn these reasons are suppressed as startup
			// noise (there is no cache to invalidate), which is covered by
			// TestPromptRebuildStartupNoiseSuppressed; here we assert the
			// routing surfaces the notice on the meaningful mid-session path.
			s.agent.SeedLastTurnUsage(provider.Usage{InputTokens: 5000})
			// Force the next rebuild to differ so it announces a prompt rebuild.
			s.agent.SetSystem("stale placeholder prompt")
			sub := s.hub.add(nil, true)
			tc.call()
			ev, _ := drainUntil(t, sub, ctrlproto.EventNotice)
			if ev.Notice == nil || ev.Notice.Kind != ctrlproto.NoticePromptRebuilt {
				t.Fatalf("%s did not trigger a prompt rebuild; got %+v", tc.name, ev.Notice)
			}
			if ev.Notice.Data["reason"] != tc.reason {
				t.Errorf("rebuild reason = %q, want %q", ev.Notice.Data["reason"], tc.reason)
			}
		})
	}
}

// TestPromptRebuildStartupNoiseSuppressed pins the startup-quiet behaviour: an
// extension asserting its tool policy before the first turn (terva-git-worktree
// withdrawing tools outside a repo) must NOT raise a "next turn starts
// uncached" banner — there is no cache to invalidate yet — but must leave a
// diagnostic trail. User-initiated rebuilds and mid-session ones still notify
// (TestWebExtHooksRefreshTriggersRebuild, TestWorkspaceSetApprovalPlan…).
func TestPromptRebuildStartupNoiseSuppressed(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	args := build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true}
	w, err := NewWorkspace(args, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	defer w.Close()

	var diagMu sync.Mutex
	var diags []string
	w.SetDiag(func(msg string) {
		diagMu.Lock()
		diags = append(diags, msg)
		diagMu.Unlock()
	})

	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	s := w.existing(info.ID)
	if s == nil {
		t.Fatal("session not materialized")
	}

	// No SeedLastTurnUsage: this is the pre-first-turn state. Force a real diff
	// so the rebuild isn't skipped as a no-op, then drive the extension hook.
	s.agent.SetSystem("stale placeholder prompt")
	sub := s.hub.add(nil, true)
	webExtHooks{s: s}.RefreshTools() // reason "tool-withdrawal", tokens == 0

	// The broadcast is synchronous, so any notice is already buffered: assert
	// none is a prompt-rebuilt banner.
	select {
	case ev := <-sub:
		if ev.Type == ctrlproto.EventNotice && ev.Notice != nil && ev.Notice.Kind == ctrlproto.NoticePromptRebuilt {
			t.Fatalf("startup tool-withdrawal should be suppressed, got banner: %+v", ev.Notice)
		}
	default:
	}

	diagMu.Lock()
	defer diagMu.Unlock()
	found := false
	for _, d := range diags {
		if strings.Contains(d, "tool-withdrawal") && strings.Contains(d, "before first turn") {
			found = true
		}
	}
	if !found {
		t.Errorf("suppressed rebuild left no diagnostic trail; diags = %v", diags)
	}
}
