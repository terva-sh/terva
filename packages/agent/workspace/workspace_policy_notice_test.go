package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/internal/coretest"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/session"
	"terva.sh/terva/packages/testsupport"
)

// A project config that cannot be parsed loses its deny and ask rules. The
// daemon used to say so only through the session-build diagnostic, which lands
// on stderr before the TUI owns the screen, in a muted note, or in the daemon's
// log, and never in a web client. A session built over such a config now holds
// the warning for its clients.
func TestASessionBuiltOverABrokenProjectConfigHoldsItsWarning(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	cwd := testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(cwd, ".terva"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(cwd, ".terva", "config.json")
	if err := os.WriteFile(cfg, []byte(`{"permissions": [{"tool": "bash", "decision": "deny"},]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd, NoExt: true, NoMCP: true}, "test")
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

	s.mu.Lock()
	pending := append([]string(nil), s.policyNotice...)
	s.mu.Unlock()
	var found bool
	for _, m := range pending {
		if strings.Contains(m, "project config unreadable") && strings.Contains(m, cfg) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the session holds no warning naming %s for its clients: %q", cfg, pending)
	}
}

// oneReplyClient answers every request with a short final reply.
type oneReplyClient struct{}

func (oneReplyClient) Name() string { return "one-reply-fake" }

func (oneReplyClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 1)
	out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "ok"}},
	}}
	close(out)
	return out, nil
}

// The held warnings go to every attached client as an error notice when the
// first turn starts, because a notice is not replayed in a snapshot and no
// client is attached while the session is built. They go once: the second turn
// carries no repeat. The TUI and the web client both render notices, so this
// one path reaches both.
func TestPolicyWarningsReachClientsOnTheFirstTurnOnly(t *testing.T) {
	tmp := testsupport.TempDir(t)
	sess, err := session.NewSession(tmp, tmp, "p", "m", "test")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	s := &wsSession{
		id:    "policy-notice",
		ws:    &Workspace{ctx: context.Background(), diag: func(string) {}},
		hub:   newWSHub(),
		sess:  sess,
		agent: coretest.NewAgent(oneReplyClient{}, "m", "", core.Registry{}),
		title: "titled",
	}
	s.agent.AddEventObserver(func(ev core.AgentEvent) {
		s.broadcast(ctrlproto.ConversationEvent(core.EventToWire(ev)))
	})
	const warning = "config: project config unreadable, so none of its settings apply until it is fixed. These restrictions are NOT applied: permissions (its deny and ask rules), disable_extensions, disable_mcp, disable_context_extensions, tickets, project_scoped. Extensions and MCP servers it disables can start: parse /x/.terva/config.json: bad"
	s.setPolicyWarnings([]string{warning})

	sub := s.hub.add(nil, true)
	if err := s.prompt("hi", nil, core.UserMessageExtras{}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	ev, _ := drainUntil(t, sub, ctrlproto.EventNotice, "done")
	if ev.Type != ctrlproto.EventNotice {
		t.Fatalf("the first turn finished before any notice; want the policy warning first")
	}
	if ev.Notice == nil || ev.Notice.Level != "error" || ev.Notice.Text != warning {
		t.Fatalf("notice = %+v, want an error notice carrying the warning", ev.Notice)
	}
	drainUntil(t, sub, "done")
	waitIdle(t, s)

	if err := s.prompt("again", nil, core.UserMessageExtras{}); err != nil {
		t.Fatalf("second prompt: %v", err)
	}
	_, seen := drainUntil(t, sub, "done")
	for _, e := range seen {
		if e.Type == ctrlproto.EventNotice && e.Notice != nil && e.Notice.Text == warning {
			t.Fatal("the policy warning was sent again on the second turn")
		}
	}
}

// A live rule reload can find a broken project config while no client is
// attached, a headless session or one whose clients have all left. The warning
// must then wait for the next turn rather than go out to no one. With a client
// attached it goes out at once.
func TestAReloadWarningWaitsForAClient(t *testing.T) {
	s := &wsSession{id: "reload-notice", hub: newWSHub()}
	const warning = "config: project config unreadable, so none of its settings apply until it is fixed. These restrictions are NOT applied: permissions (its deny and ask rules), disable_extensions, disable_mcp, disable_context_extensions, tickets, project_scoped. Extensions and MCP servers it disables can start: parse /x/.terva/config.json: bad"

	s.reloadPolicyWarnings([]string{warning})
	s.mu.Lock()
	pending := append([]string(nil), s.policyNotice...)
	s.mu.Unlock()
	if len(pending) != 1 || pending[0] != warning {
		t.Fatalf("with no client attached the warning was not kept for the next turn: %q", pending)
	}

	sub := s.hub.add(nil, true)
	s.reloadPolicyWarnings([]string{warning})
	select {
	case ev := <-sub:
		if ev.Type != ctrlproto.EventNotice || ev.Notice == nil || ev.Notice.Text != warning {
			t.Fatalf("got %+v, want the warning as a notice", ev)
		}
	default:
		t.Fatal("with a client attached the warning did not go out at once")
	}
	s.mu.Lock()
	left := len(s.policyNotice)
	s.mu.Unlock()
	if left != 0 {
		t.Fatalf("a warning already sent is still pending: %d left", left)
	}
}
