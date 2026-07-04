package agent

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/agent/modes"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

func newTestSession() *wsSession {
	return &wsSession{
		id:       "test",
		hub:      newWSHub(),
		pendPerm: map[string]chan core.ConfirmDecision{},
		pendAsk:  map[string]chan core.UserAnswer{},
		permReq:  map[string]ctrlproto.PermissionRequest{},
		askReq:   map[string]ctrlproto.AskRequest{},
	}
}

func recvEvent(t *testing.T, ch <-chan ctrlproto.Event) ctrlproto.Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for event")
		return ctrlproto.Event{}
	}
}

func TestWSHubFanoutAndSnapshotFirst(t *testing.T) {
	h := newWSHub()
	snap := ctrlproto.SnapshotEvent(ctrlproto.Snapshot{Session: ctrlproto.SessionInfo{ID: "s1"}})
	a := h.add(func() ctrlproto.Event { return snap })
	b := h.add(nil)

	// The snapshot must be the very first event the new subscriber sees.
	if ev := <-a; ev.Type != ctrlproto.EventSnapshot {
		t.Fatalf("want snapshot first, got %q", ev.Type)
	}
	// A broadcast reaches every subscriber.
	h.broadcast(ctrlproto.ConversationEvent(core.WireEvent{Type: "text_delta", Delta: "x"}))
	if ev := <-a; ev.Delta != "x" {
		t.Errorf("subscriber a missed delta: %+v", ev)
	}
	if ev := <-b; ev.Delta != "x" {
		t.Errorf("subscriber b missed delta: %+v", ev)
	}
	// remove closes the channel.
	h.remove(a)
	if _, ok := <-a; ok {
		t.Error("removed subscriber channel should be closed")
	}
}

func TestWebConfirmerApproveWins(t *testing.T) {
	s := newTestSession()
	sub := s.hub.add(nil)
	s.mu.Lock()
	s.turnCtx = t.Context()
	s.curCallID = "call_42"
	s.mu.Unlock()

	result := make(chan core.ConfirmDecision, 1)
	go func() { result <- (&webConfirmer{s: s}).Confirm("bash", "ls -la") }()

	ev := recvEvent(t, sub)
	if ev.Type != ctrlproto.EventPermissionRequest || ev.Permission == nil {
		t.Fatalf("want permission_request, got %+v", ev)
	}
	if ev.Permission.CallID != "call_42" || ev.Permission.Tool != "bash" || ev.Permission.Preview != "ls -la" {
		t.Fatalf("permission payload: %+v", ev.Permission)
	}

	s.approve("call_42", core.ConfirmDecision{Allow: true, RememberTool: true})
	select {
	case d := <-result:
		if !d.Allow || !d.RememberTool {
			t.Errorf("decision not forwarded: %+v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Confirm did not return after approve")
	}
	if ev := recvEvent(t, sub); ev.Type != ctrlproto.EventPermissionResolved || ev.Resolved == nil || ev.Resolved.CallID != "call_42" {
		t.Errorf("want permission_resolved for call_42, got %+v", ev)
	}
}

// TestPendingPermissionRecordedForSnapshot guards the reconnect-durability fix:
// while a Confirm is parked, the request is recorded (so a fresh snapshot can
// re-surface the dialog) and cleared once resolved.
func TestPendingPermissionRecordedForSnapshot(t *testing.T) {
	s := newTestSession()
	s.mu.Lock()
	s.turnCtx = t.Context()
	s.curCallID = "c9"
	s.mu.Unlock()
	sub := s.hub.add(nil)

	go (&webConfirmer{s: s}).Confirm("bash", "ls -la")

	if ev := recvEvent(t, sub); ev.Type != ctrlproto.EventPermissionRequest {
		t.Fatalf("want permission_request, got %q", ev.Type)
	}
	s.mu.Lock()
	req, ok := s.permReq["c9"]
	s.mu.Unlock()
	if !ok || req.Tool != "bash" {
		t.Fatalf("pending permission not recorded for snapshot: %+v", req)
	}

	s.approve("c9", core.ConfirmDecision{Allow: true})
	if ev := recvEvent(t, sub); ev.Type != ctrlproto.EventPermissionResolved {
		t.Fatalf("want permission_resolved, got %q", ev.Type)
	}
	s.mu.Lock()
	_, still := s.permReq["c9"]
	s.mu.Unlock()
	if still {
		t.Error("pending permission not cleared after resolve")
	}
}

func TestWebConfirmerCancelFailsClosed(t *testing.T) {
	s := newTestSession()
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.turnCtx = ctx
	s.curCallID = "c1"
	s.mu.Unlock()

	result := make(chan core.ConfirmDecision, 1)
	go func() { result <- (&webConfirmer{s: s}).Confirm("bash", "rm -rf /") }()
	// Give Confirm a moment to park, then cancel the turn.
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case d := <-result:
		if d.Allow {
			t.Error("a cancelled Confirm must deny (fail closed)")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Confirm did not return after cancel")
	}
}

func TestWebAskerAnswerWins(t *testing.T) {
	s := newTestSession()
	sub := s.hub.add(nil)

	result := make(chan core.UserAnswer, 1)
	go func() {
		ans, _ := (&webAsker{s: s}).Ask(context.Background(), core.UserQuestion{
			Question: "Which approach?", Options: []string{"a", "b"}, AllowCustom: true,
		})
		result <- ans
	}()

	ev := recvEvent(t, sub)
	if ev.Type != ctrlproto.EventAskRequest || ev.Ask == nil {
		t.Fatalf("want ask_request, got %+v", ev)
	}
	if ev.Ask.Question != "Which approach?" || len(ev.Ask.Options) != 2 || !ev.Ask.AllowCustom {
		t.Fatalf("ask payload: %+v", ev.Ask)
	}
	s.answer(ev.Ask.AskID, core.UserAnswer{Answer: "b"})
	select {
	case ans := <-result:
		if ans.Answer != "b" {
			t.Errorf("answer not forwarded: %+v", ans)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ask did not return after answer")
	}
	if ev := recvEvent(t, sub); ev.Type != ctrlproto.EventAskResolved {
		t.Errorf("want ask_resolved, got %+v", ev)
	}
}

// TestWorkspaceSessionGroup exercises the session-group CRUD over real session
// files without a live agent (no credentials needed): list, rename, delete,
// usage, and the not-found path.
func TestWorkspaceSessionGroup(t *testing.T) {
	tmp := t.TempDir()
	w := &Workspace{root: tmp, cwd: tmp, version: "test", sessions: map[string]*wsSession{}}
	ctx := context.Background()

	// A message is appended so Close does not prune the file as empty+fresh.
	msg := provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hello"}}}

	s1, err := core.NewSession(tmp, tmp, "anthropic", "m1", "test")
	if err != nil {
		t.Fatalf("NewSession 1: %v", err)
	}
	_ = s1.AppendMessage(msg)
	_ = core.RenameSession(s1.Path, "first")
	_ = s1.Close()
	s2, err := core.NewSession(tmp, tmp, "anthropic", "m2", "test")
	if err != nil {
		t.Fatalf("NewSession 2: %v", err)
	}
	_ = s2.AppendMessage(msg)
	_ = s2.Close()

	id1 := sessionIDFromPath(s1.Path)
	id2 := sessionIDFromPath(s2.Path)

	list, err := w.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(list))
	}
	if titleOf(list, id1) != "first" {
		t.Errorf("session 1 title = %q, want %q", titleOf(list, id1), "first")
	}

	if err := w.RenameSession(ctx, id1, "renamed"); err != nil {
		t.Fatalf("RenameSession: %v", err)
	}
	list, _ = w.Sessions(ctx)
	if titleOf(list, id1) != "renamed" {
		t.Errorf("after rename, title = %q, want %q", titleOf(list, id1), "renamed")
	}

	if err := w.DeleteSession(ctx, id2); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	list, _ = w.Sessions(ctx)
	if len(list) != 1 {
		t.Fatalf("after delete, want 1 session, got %d", len(list))
	}

	if _, err := w.Usage(ctx, id1); err != nil {
		t.Errorf("Usage: %v", err)
	}

	if err := w.DeleteSession(ctx, "does-not-exist"); err == nil {
		t.Error("deleting a missing session should error")
	}
	if _, err := w.Usage(ctx, "does-not-exist"); err == nil {
		t.Error("usage of a missing session should error")
	}
}

// TestDeleteEmptyLiveSession guards the regression where deleting a
// materialized-but-empty session failed: close() prunes the empty fresh file,
// so the follow-up os.Remove sees nothing and must not report "no session".
func TestDeleteEmptyLiveSession(t *testing.T) {
	tmp := t.TempDir()
	w := &Workspace{root: tmp, cwd: tmp, version: "test", sessions: map[string]*wsSession{}}
	ctx := context.Background()

	s, err := core.NewSession(tmp, tmp, "p", "m", "test") // empty + fresh
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	id := sessionIDFromPath(s.Path)
	// Register it as if materialized (minimal live session — no agent needed).
	w.sessions[id] = &wsSession{id: id, ws: w, sess: s, hub: newWSHub()}

	if err := w.DeleteSession(ctx, id); err != nil {
		t.Fatalf("deleting an empty live session should succeed, got %v", err)
	}
	if _, ok := w.sessions[id]; ok {
		t.Error("session still registered after delete")
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Error("session file still present after delete")
	}
	if err := w.DeleteSession(ctx, "never-existed"); err == nil {
		t.Error("deleting an unknown session should return not-found")
	}
}

func TestFirstUserText(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "hi"}}},
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "  refactor the parser  "}}},
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "second"}}},
	}
	if got := firstUserText(msgs); got != "refactor the parser" {
		t.Errorf("firstUserText = %q, want %q", got, "refactor the parser")
	}
	if got := firstUserText(nil); got != "" {
		t.Errorf("firstUserText(nil) = %q, want empty", got)
	}
}

func TestCleanTitle(t *testing.T) {
	cases := map[string]string{
		"\"Refactor the parser.\"": "Refactor the parser",
		"Fix the WAL bug!":         "Fix the WAL bug",
		"Debug session\nline two":  "Debug session line two",
		"  `title in ticks`  ":     "title in ticks",
	}
	for in, want := range cases {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
	long := cleanTitle(strings.Repeat("word ", 30))
	if !strings.HasSuffix(long, "…") {
		t.Errorf("cleanTitle should cap+ellipsize long input, got %q", long)
	}
}

// TestSettleTitleFallbackBroadcasts covers the first-line title path (AutoTitle
// off): applyTitle persists the name to the session file and pushes a
// session_updated event so open clients update without a refresh.
func TestSettleTitleFallbackBroadcasts(t *testing.T) {
	tmp := t.TempDir()
	w := &Workspace{root: tmp, cwd: tmp, version: "test", sessions: map[string]*wsSession{}}
	sess, err := core.NewSession(tmp, tmp, "p", "m", "test")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = sess.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "help me refactor the parser"}}})
	s := &wsSession{id: sessionIDFromPath(sess.Path), ws: w, sess: sess, hub: newWSHub()}

	sub := s.hub.add(nil)
	s.applyTitle("help me refactor the parser")

	ev := recvEvent(t, sub)
	if ev.Type != ctrlproto.EventSessionUpdated || ev.Info == nil {
		t.Fatalf("want session_updated, got %+v", ev)
	}
	if ev.Info.Title != "help me refactor the parser" {
		t.Errorf("broadcast title = %q", ev.Info.Title)
	}
	if got := core.DescribeSessions(tmp, tmp); len(got) == 0 || got[0].Title != "help me refactor the parser" {
		t.Errorf("title not persisted to file: %+v", got)
	}
}

// TestSettleTitleSkipsWhenTitled guards that a session that already has a title
// (user rename or a prior turn) neither regenerates nor broadcasts.
func TestSettleTitleSkipsWhenTitled(t *testing.T) {
	s := &wsSession{id: "x", hub: newWSHub(), title: "already named"}
	sub := s.hub.add(nil)
	s.settleTitle(context.Background())
	select {
	case ev := <-sub:
		t.Fatalf("titled session should not broadcast, got %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestSetQueueBroadcasts covers the queue.set path behind editing/cancelling
// queued messages: it replaces the agent queue and broadcasts the new list.
func TestSetQueueBroadcasts(t *testing.T) {
	s := &wsSession{id: "x", hub: newWSHub(), agent: core.NewAgent(nil, "fake", "", core.Registry{})}
	sub := s.hub.add(nil)

	s.setQueue([]string{"a", "b"})
	ev := recvEvent(t, sub)
	if ev.Type != ctrlproto.EventQueueUpdated {
		t.Fatalf("want queue_updated, got %q", ev.Type)
	}
	if len(ev.Queued) != 2 || ev.Queued[0] != "a" || ev.Queued[1] != "b" {
		t.Fatalf("queued = %v, want [a b]", ev.Queued)
	}

	// Cancelling one is a set of the remaining list.
	s.setQueue([]string{"b"})
	ev2 := recvEvent(t, sub)
	if len(ev2.Queued) != 1 || ev2.Queued[0] != "b" {
		t.Fatalf("after cancel queued = %v, want [b]", ev2.Queued)
	}
}

// TestContextBreakdown covers the /context size accounting: system + transcript
// bytes are summed into the total and the largest message is discoverable.
func TestContextBreakdown(t *testing.T) {
	ag := core.NewAgent(nil, "fake", "", core.Registry{})
	ag.System = "you are a helpful assistant"
	ag.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: strings.Repeat("x", 800)}}},
	})
	s := &wsSession{id: "x", hub: newWSHub(), agent: ag}

	b := s.contextBreakdown()
	if b.SystemBytes != len(ag.System) {
		t.Errorf("system bytes = %d, want %d", b.SystemBytes, len(ag.System))
	}
	if len(b.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(b.Messages))
	}
	if b.TranscriptBytes <= 800 {
		t.Errorf("transcript bytes = %d, want > 800", b.TranscriptBytes)
	}
	if b.TotalBytes != b.SystemBytes+b.ToolBytes+b.ExtBytes+b.TranscriptBytes {
		t.Errorf("total %d != sum of parts", b.TotalBytes)
	}
	if b.Messages[1].Bytes <= b.Messages[0].Bytes {
		t.Errorf("expected message 1 (the long one) to be largest: %+v", b.Messages)
	}
}

// TestSurfaceListAndGet covers the core panes: the registry always offers the
// combined context+usage pane (the old separate "usage" pane folded in), and it
// returns its typed payload with the usage picture attached.
func TestSurfaceListAndGet(t *testing.T) {
	s := &wsSession{id: "x", hub: newWSHub(), agent: core.NewAgent(nil, "fake", "", core.Registry{}), extPanels: map[string]*webPanel{}}
	metas := s.surfaceList()
	if !hasSurface(metas, "context") {
		t.Fatalf("want context surface, got %+v", metas)
	}
	if hasSurface(metas, "usage") {
		t.Fatalf("usage surface should be folded into context, got %+v", metas)
	}
	ctx, err := s.surface("context")
	if err != nil || ctx.Kind != "context" || ctx.Context == nil {
		t.Fatalf("context surface: %+v err=%v", ctx, err)
	}
	// The combined pane carries the cumulative usage struct (the old usage pane).
	if ctx.Context.Cumulative.CostUSD < 0 {
		t.Errorf("context surface missing usage picture: %+v", ctx.Context)
	}
	if _, err := s.surface("usage"); err == nil {
		t.Error("removed usage surface should error")
	}
	if _, err := s.surface("nope"); err == nil {
		t.Error("unknown surface should error")
	}
}

// TestSurfaceTitlesLocalized proves the server localizes its own ctrlproto
// display strings to the process locale: with Finnish active, surface titles and
// settings labels come off the wire already translated (the client renders them
// verbatim). Mutates the global i18n language, so it resets on return and does
// not run in parallel.
func TestSurfaceTitlesLocalized(t *testing.T) {
	if err := i18n.Configure("fi", ""); err != nil {
		t.Fatalf("configure fi: %v", err)
	}
	defer i18n.Configure("en", "")

	s := newTestSession()
	s.agent = core.NewAgent(nil, "fake", "", core.Registry{})

	ctx, err := s.surface("context")
	if err != nil {
		t.Fatalf("context surface: %v", err)
	}
	if ctx.Title != "Käyttö" {
		t.Errorf("surface title not localized: got %q, want %q", ctx.Title, "Käyttö")
	}

	set, err := s.surface("settings")
	if err != nil || set.Settings == nil || len(set.Settings.Items) == 0 {
		t.Fatalf("settings surface: %+v err=%v", set, err)
	}
	if set.Settings.Items[0].Label != "Hyväksyntätila" {
		t.Errorf("settings label not localized: got %q, want %q", set.Settings.Items[0].Label, "Hyväksyntätila")
	}
	// Option labels are declared at init (i18n.M) and translated at render time.
	if opts := set.Settings.Items[0].Options; len(opts) == 0 || opts[0].Label != "suunnitelma — vain luku" {
		t.Errorf("option label not localized: %+v", opts)
	}

	// Byte-identity invariant: back in English, titles are the source strings.
	i18n.Configure("en", "")
	if en, _ := s.surface("context"); en.Title != "Usage" {
		t.Errorf("english title should be the source: got %q", en.Title)
	}
}

// TestExtWidgetSurface covers the rich-widget panel path: an extension panel
// carrying a widget tree surfaces as kind=widgets (meta + payload), with the
// extproto vocabulary mapped to the control-plane one, and falls back to
// kind=panel lines when it has none.
func TestExtWidgetSurface(t *testing.T) {
	s := &wsSession{id: "x", hub: newWSHub(), agent: core.NewAgent(nil, "fake", "", core.Registry{}), extPanels: map[string]*webPanel{}}

	s.paneOpen("todo", extproto.PanelSpec{
		ID:    "main",
		Title: "Todos",
		Lines: []string{"fallback line"},
		Widgets: []extproto.Widget{
			{Type: "meter", Label: "done", Value: 1, Max: 3},
			{Type: "list", Items: []extproto.WidgetItem{{Text: "a", ActionID: "toggle:0"}, {Text: "b"}}},
		},
	})
	sid := "ext:todo:main"

	if !hasSurfaceKind(s.surfaceList(), sid, "widgets") {
		t.Fatalf("widget panel should surface as kind=widgets in the registry")
	}
	sf, err := s.surface(sid)
	if err != nil || sf.Kind != "widgets" {
		t.Fatalf("surface: kind=%q err=%v", sf.Kind, err)
	}
	if len(sf.Widgets) != 2 || sf.Widgets[0].Type != "meter" || sf.Widgets[0].Max != 3 {
		t.Fatalf("widgets not mapped: %+v", sf.Widgets)
	}
	if len(sf.Widgets[1].Items) != 2 || sf.Widgets[1].Items[0].ActionID != "toggle:0" {
		t.Errorf("list items not mapped: %+v", sf.Widgets[1].Items)
	}

	// Dropping the widgets (a lines-only re-render) falls back to kind=panel.
	s.paneUpdate("todo", "main", "Todos", []string{"just lines"}, "", nil)
	if sf, _ := s.surface(sid); sf.Kind != "panel" || sf.Panel == nil {
		t.Errorf("lines-only panel should be kind=panel, got %q", sf.Kind)
	}
}

// TestCommandResponseActions covers dispatching an extension command from the
// commands pane: open_panel surfaces a pane, display/insert become one-shot
// notices (the web has no command line to fill, so insert degrades to a note),
// a command-level error becomes an error notice, and noop broadcasts nothing.
func TestCommandResponseActions(t *testing.T) {
	s := &wsSession{id: "x", hub: newWSHub(), agent: core.NewAgent(nil, "fake", "", core.Registry{}), extPanels: map[string]*webPanel{}}
	sub := s.hub.add(nil)

	// open_panel opens a pane through the existing panel machinery.
	s.applyCommandResponse("todo", extproto.CommandResponseFromExt{
		Action:    "open_panel",
		OpenPanel: &extproto.PanelSpec{ID: "main", Title: "Todos", Lines: []string{"a"}},
	})
	if _, err := s.surface("ext:todo:main"); err != nil {
		t.Fatalf("open_panel should have opened a surface: %v", err)
	}
	recvEvent(t, sub) // surfaces_changed
	recvEvent(t, sub) // surface_updated

	// display → info notice attributed to the extension.
	s.applyCommandResponse("todo", extproto.CommandResponseFromExt{Action: "display", Display: "No todos."})
	ev := recvEvent(t, sub)
	if ev.Type != ctrlproto.EventNotice || ev.Notice == nil {
		t.Fatalf("display should broadcast a notice, got %+v", ev)
	}
	if ev.Notice.Level != "info" || ev.Notice.Ext != "todo" || ev.Notice.Text != "No todos." {
		t.Errorf("display notice payload: %+v", ev.Notice)
	}

	// insert has no shared composer to fill on the web → degrades to an info note.
	s.applyCommandResponse("todo", extproto.CommandResponseFromExt{Action: "insert", Insert: "draft"})
	if ev := recvEvent(t, sub); ev.Type != ctrlproto.EventNotice || ev.Notice.Level != "info" || ev.Notice.Text != "draft" {
		t.Errorf("insert should degrade to an info notice, got %+v", ev)
	}

	// A command-level error wins over the action → error notice.
	s.applyCommandResponse("todo", extproto.CommandResponseFromExt{Action: "display", Error: "boom"})
	if ev := recvEvent(t, sub); ev.Type != ctrlproto.EventNotice || ev.Notice.Level != "error" || ev.Notice.Text != "boom" {
		t.Errorf("error should broadcast an error notice, got %+v", ev)
	}

	// noop broadcasts nothing.
	s.applyCommandResponse("todo", extproto.CommandResponseFromExt{Action: "noop"})
	select {
	case ev := <-sub:
		t.Errorf("noop should broadcast nothing, got %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

// TestWorkspaceRestartGated confirms the control.restart method refuses when the
// self-restart capability was never enabled (the default) — it must not re-exec
// the daemon just because a client asked. relaunch is process-global and left
// disabled here, so this never actually restarts the test binary.
func TestWorkspaceRestartGated(t *testing.T) {
	w := &Workspace{}
	err := w.Restart(context.Background())
	if err == nil {
		t.Fatal("Restart should error when self-restart is disabled")
	}
	var ce *ctrlproto.Error
	if !errors.As(err, &ce) || ce.Code != ctrlproto.CodeUnsupported {
		t.Fatalf("want CodeUnsupported, got %v", err)
	}
}

// TestWorkspaceCompact covers user-driven compaction's two no-model-call paths:
// an empty transcript reports a benign notice (no error), and a running turn is
// refused with ErrBusy. The actual summarize+replace path needs a live model, so
// it's exercised by core's own compaction tests.
func TestWorkspaceCompact(t *testing.T) {
	s := &wsSession{id: "x", hub: newWSHub(), agent: core.NewAgent(nil, "fake", "", core.Registry{})}
	sub := s.hub.add(nil)

	// Empty transcript → benign "nothing to compact" notice, no error.
	if err := s.compact(context.Background()); err != nil {
		t.Fatalf("compact(empty): %v", err)
	}
	if ev := recvEvent(t, sub); ev.Type != ctrlproto.EventNotice || ev.Notice == nil {
		t.Fatalf("empty compact should broadcast a notice, got %+v", ev)
	}

	// A running turn blocks compaction.
	s.mu.Lock()
	s.turnCancel = func() {}
	s.mu.Unlock()
	if err := s.compact(context.Background()); !errors.Is(err, ctrlproto.ErrBusy) {
		t.Fatalf("compact while busy should be ErrBusy, got %v", err)
	}
}

// TestExtensionStatus covers the extensions-pane status derivation: running wins,
// then gated (untrusted project), then disabled (config off), else stopped
// (enabled but not running = crashed). And the crash-note only shows when off.
func TestExtensionStatus(t *testing.T) {
	cases := []struct {
		e    modes.ExtInfo
		want string
	}{
		{modes.ExtInfo{Running: true, Effective: true}, "running"},
		{modes.ExtInfo{Running: true, ProjectGated: true}, "running"}, // running wins over gated
		{modes.ExtInfo{Effective: true}, "stopped"},                   // enabled, not running → crashed
		{modes.ExtInfo{Effective: false}, "disabled"},
		{modes.ExtInfo{ProjectGated: true}, "gated"},
	}
	for _, c := range cases {
		if got := extensionStatus(c.e); got != c.want {
			t.Errorf("extensionStatus(%+v) = %q, want %q", c.e, got, c.want)
		}
	}
	if n := extensionNote(modes.ExtInfo{Running: true, LastLog: "x"}); n != "" {
		t.Errorf("a running ext should have no note, got %q", n)
	}
	if n := extensionNote(modes.ExtInfo{Running: false, LastLog: "boom"}); n != "boom" {
		t.Errorf("a stopped ext should surface its log tail, got %q", n)
	}
}

// TestPermissionsSurface covers the permissions inspector: the view maps the
// gate's mode + rules, reflects the live allow-all grant, and revoke_all clears
// it (broadcasting a surface refresh); unknown actions error.
func TestPermissionsSurface(t *testing.T) {
	pol := &core.PermissionPolicy{
		Mode: core.ApprovalWorkspace,
		Rules: []core.PermissionRule{
			{Tool: "bash", Decision: core.RuleAsk, Source: "user"},
			{Tool: "read", Decision: core.RuleAllow, Source: "builtin"},
		},
	}
	s := &wsSession{id: "x", hub: newWSHub(), gate: core.NewPolicyGate(pol, nil)}

	v := s.permissionsView()
	if v.Mode != string(core.ApprovalWorkspace) {
		t.Errorf("mode = %q, want workspace", v.Mode)
	}
	if len(v.Rules) != 2 || v.Rules[0].Tool != "bash" || v.Rules[0].Decision != "ask" || v.Rules[1].Decision != "allow" {
		t.Fatalf("rules not mapped: %+v", v.Rules)
	}
	if v.AllowAll {
		t.Error("allowAll should start false")
	}

	// A live allow-all grant shows, then revoke_all clears it + refreshes clients.
	s.gate.AllowAll()
	if got := s.permissionsView(); !got.AllowAll {
		t.Error("allowAll should be true after AllowAll()")
	}
	sub := s.hub.add(nil)
	if err := s.permissionsAction("revoke_all", nil); err != nil {
		t.Fatalf("revoke_all: %v", err)
	}
	if got := s.permissionsView(); got.AllowAll {
		t.Error("revoke_all should clear allowAll")
	}
	if ev := recvEvent(t, sub); ev.Type != ctrlproto.EventSurfaceUpdated || ev.SurfaceID != "permissions" {
		t.Errorf("want surface_updated(permissions), got %+v", ev)
	}

	// revoking an ungranted tool is a no-op (no error); an unknown action errors.
	if err := s.permissionsAction("revoke", map[string]string{"tool": "bash"}); err != nil {
		t.Errorf("revoke no-op should not error: %v", err)
	}
	recvEvent(t, sub) // drain the revoke's surface_updated
	if err := s.permissionsAction("bogus", nil); err == nil {
		t.Error("unknown action should error")
	}
}

// TestExtensionsToggleAction covers the enable/disable persist round-trip: a
// toggle writes the project config's disable list and broadcasts a refresh. The
// live-apply half (ApplyOne + tool rebuild) needs a real subprocess, so extMgr
// is nil here (applyExtensionChangeLive no-ops), isolating the persist path.
func TestExtensionsToggleAction(t *testing.T) {
	dir := t.TempDir()
	s := &wsSession{id: "x", hub: newWSHub(), cwd: dir}

	if err := s.extensionsAction("bogus", map[string]string{"name": "foo"}); err == nil {
		t.Error("unknown action should error")
	}
	if err := s.extensionsAction("toggle", map[string]string{}); err == nil {
		t.Error("missing name should error")
	}

	sub := s.hub.add(nil)
	// Disable → foo lands in the project disable list.
	if err := s.extensionsAction("toggle", map[string]string{"name": "foo", "enabled": "false"}); err != nil {
		t.Fatalf("toggle off: %v", err)
	}
	if pc, err := LoadProjectConfig(dir); err != nil || pc == nil || !slices.Contains(pc.DisableExtensions, "foo") {
		t.Fatalf("foo should be project-disabled, got %+v (err %v)", pc, err)
	}
	if ev := recvEvent(t, sub); ev.Type != ctrlproto.EventSurfaceUpdated || ev.SurfaceID != "extensions" {
		t.Errorf("want surface_updated(extensions), got %+v", ev)
	}

	// Enable → foo removed from the disable list.
	if err := s.extensionsAction("toggle", map[string]string{"name": "foo", "enabled": "true"}); err != nil {
		t.Fatalf("toggle on: %v", err)
	}
	recvEvent(t, sub)
	if pc, _ := LoadProjectConfig(dir); pc != nil && slices.Contains(pc.DisableExtensions, "foo") {
		t.Errorf("foo should be re-enabled, still disabled: %+v", pc.DisableExtensions)
	}
}

// TestLoreView covers the lore inspector mapping: entries carry name/keys/
// constant/source + full content (client truncates for display).
func TestLoreView(t *testing.T) {
	s := &wsSession{loreEntries: []lore.Entry{
		{Name: "greeting", Keys: []string{"hi", "hello"}, Source: "a.md", Content: "hello there"},
		{Name: "always-on", Constant: true, Content: "always"},
	}}
	v := s.loreView()
	if len(v.Entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(v.Entries))
	}
	if e := v.Entries[0]; e.Name != "greeting" || len(e.Keys) != 2 || e.Constant || e.Content != "hello there" {
		t.Errorf("entry 0 mismap: %+v", e)
	}
	if e := v.Entries[1]; !e.Constant || e.Content != "always" {
		t.Errorf("entry 1 mismap: %+v", e)
	}
}

// TestLoreEditing covers the save/delete round-trip: a save writes a parseable
// user lore file (re-discovered on load) and marks it editable; delete removes
// it; validation rejects a keyless non-constant entry.
func TestLoreEditing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TERVA_HOME", home)

	if err := saveLoreEntry(home, "user", "Deploy Steps", []string{"deploy", "release"}, false, "Run just release-cut."); err != nil {
		t.Fatalf("saveLoreEntry: %v", err)
	}
	if _, err := os.Stat(loreFile(home, "user", "Deploy Steps")); err != nil {
		t.Fatal("saved entry should be a user file")
	}
	// It parses back as a real entry.
	got, _, _ := lore.Discover(home, home, false)
	var found *lore.Entry
	for i := range got {
		if got[i].Name == "Deploy Steps" {
			found = &got[i]
		}
	}
	if found == nil || len(found.Keys) != 2 || found.Content == "" {
		t.Fatalf("discovered entry wrong: %+v", found)
	}
	// A keyless non-constant entry is rejected by validation.
	if err := saveLoreEntry(home, "user", "Bad", nil, false, "body"); err == nil {
		t.Error("keyless non-constant entry should fail validation")
	}
	// Delete removes it.
	if err := deleteLoreEntry(home, "user", "Deploy Steps"); err != nil {
		t.Fatalf("deleteLoreEntry: %v", err)
	}
	if _, err := os.Stat(loreFile(home, "user", "Deploy Steps")); err == nil {
		t.Error("entry should be gone after delete")
	}
}

// TestMCPStatus covers the MCP-pane status derivation: disabled (config off) and
// gated win over liveness; a should-run-but-not-connected server with a startup
// error is failed, else stopped; connected is running.
func TestMCPStatus(t *testing.T) {
	cases := []struct {
		m    modes.MCPInfo
		want string
	}{
		{modes.MCPInfo{Connected: true, Effective: true}, "running"},
		{modes.MCPInfo{UserDisabled: true}, "disabled"},
		{modes.MCPInfo{ProjectDisabled: true}, "disabled"},
		{modes.MCPInfo{ProjectGated: true}, "gated"},
		{modes.MCPInfo{Effective: true, StartupError: "boom"}, "failed"},
		{modes.MCPInfo{Effective: true}, "stopped"},
	}
	for _, c := range cases {
		if got := mcpStatus(c.m); got != c.want {
			t.Errorf("mcpStatus(%+v) = %q, want %q", c.m, got, c.want)
		}
	}
}

// TestPermissionsRuleAction covers user-rule add/remove: the rule persists to
// config AND applies live to the session's gate (a deny rule blocks the tool on
// the next Check — verified without a model). Bad decisions error; removing it
// restores the tool.
func TestPermissionsRuleAction(t *testing.T) {
	t.Setenv("TERVA_HOME", t.TempDir())
	w := &Workspace{sessions: map[string]*wsSession{}}
	// Gate with an explicit policy (so SetRules isn't a no-op) + an allow-all
	// confirmer, so a tool is allowed unless a deny RULE blocks it — isolating
	// the rule effect from builtin classification.
	gate := core.NewPolicyGate(&core.PermissionPolicy{Mode: core.ApprovalWorkspace}, allowConfirmer{})
	s := &wsSession{id: "x", hub: newWSHub(), ws: w, gate: gate, args: Args{}}
	w.sessions["x"] = s

	// A tool the confirmer would allow.
	if allowed, _, _ := gate.Check("mytool", nil, ""); !allowed {
		t.Fatal("precondition: mytool should be allowed by the confirmer")
	}

	if err := s.permissionsAction("add_rule", map[string]string{"tool": "mytool", "decision": "deny", "reason": "nope"}); err != nil {
		t.Fatalf("add_rule: %v", err)
	}
	// Live: the deny rule now blocks mytool (deny beats the confirmer).
	if allowed, _, _ := gate.Check("mytool", nil, ""); allowed {
		t.Error("deny rule should block mytool live")
	}
	if cfg, _ := LoadConfig(); len(cfg.Permissions) != 1 || cfg.Permissions[0].Tool != "mytool" {
		t.Errorf("rule not persisted: %+v", cfg.Permissions)
	}
	if err := s.permissionsAction("add_rule", map[string]string{"tool": "x", "decision": "bogus"}); err == nil {
		t.Error("bad decision should error")
	}
	// Remove restores it live.
	if err := s.permissionsAction("remove_rule", map[string]string{"tool": "mytool", "decision": "deny"}); err != nil {
		t.Fatalf("remove_rule: %v", err)
	}
	if allowed, _, _ := gate.Check("mytool", nil, ""); !allowed {
		t.Error("after removing the deny rule, mytool should be allowed again")
	}
	if cfg, _ := LoadConfig(); len(cfg.Permissions) != 0 {
		t.Errorf("rule not removed from config: %+v", cfg.Permissions)
	}
}

type allowConfirmer struct{}

func (allowConfirmer) Confirm(tool, preview string) core.ConfirmDecision {
	return core.ConfirmDecision{Allow: true}
}

// TestProjectScopeEditing covers project-scoped writes: a project permission
// rule round-trips through .terva/config.json (idempotent add, remove), and a
// project lore entry writes under .terva/lore.
func TestProjectScopeEditing(t *testing.T) {
	cwd := t.TempDir()
	rule := PermissionRuleConfig{Tool: "bash", Decision: "deny", Reason: "no"}
	if err := setProjectPermissionRule(cwd, rule, true); err != nil {
		t.Fatalf("add project rule: %v", err)
	}
	if pc, err := LoadProjectConfig(cwd); err != nil || pc == nil || len(pc.Permissions) != 1 || pc.Permissions[0].Tool != "bash" {
		t.Fatalf("project rule not written: %+v (err %v)", pc, err)
	}
	_ = setProjectPermissionRule(cwd, rule, true) // idempotent
	if pc, _ := LoadProjectConfig(cwd); len(pc.Permissions) != 1 {
		t.Errorf("duplicate project rule: %+v", pc.Permissions)
	}
	if err := setProjectPermissionRule(cwd, PermissionRuleConfig{Tool: "bash", Decision: "deny"}, false); err != nil {
		t.Fatalf("remove project rule: %v", err)
	}
	if pc, _ := LoadProjectConfig(cwd); pc != nil && len(pc.Permissions) != 0 {
		t.Errorf("project rule not removed: %+v", pc.Permissions)
	}

	if err := saveLoreEntry(cwd, "project", "Proj Lore", []string{"foo"}, false, "body"); err != nil {
		t.Fatalf("save project lore: %v", err)
	}
	if _, err := os.Stat(loreFile(cwd, "project", "Proj Lore")); err != nil {
		t.Error("project lore file should exist under .terva/lore")
	}
}

// TestLoreScopeTrust covers the trust gate on project-scope lore edits.
func TestLoreScopeTrust(t *testing.T) {
	trustedSess := &wsSession{}
	trustedSess.trusted.Store(true)
	untrustedSess := &wsSession{} // trusted defaults to false
	if sc, err := trustedSess.loreScope("project"); err != nil || sc != "project" {
		t.Errorf("trusted project scope: sc=%q err=%v", sc, err)
	}
	if _, err := untrustedSess.loreScope("project"); err == nil {
		t.Error("untrusted project scope should be refused")
	}
	if sc, err := untrustedSess.loreScope(""); err != nil || sc != "user" {
		t.Errorf("user scope should always work: sc=%q err=%v", sc, err)
	}
}

// TestWorkspaceClear covers the clear verb: it empties the live transcript,
// writes a durable empty checkpoint, and broadcasts a fresh snapshot + notice; a
// running turn blocks it (ErrBusy).
func TestWorkspaceClear(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TERVA_HOME", dir)
	sess, err := core.NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close() // release the file handle so TempDir cleanup works on Windows
	ag := core.NewAgent(nil, "fake", "", core.Registry{})
	ag.SetMessages([]provider.Message{{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hello"}}}})
	s := &wsSession{id: "x", hub: newWSHub(), agent: ag, sess: sess}
	sub := s.hub.add(nil)

	if err := s.clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if n := len(ag.Messages()); n != 0 {
		t.Fatalf("clear should empty the transcript, got %d messages", n)
	}
	if ev := recvEvent(t, sub); ev.Type != ctrlproto.EventSnapshot {
		t.Fatalf("clear should broadcast a snapshot first, got %q", ev.Type)
	}
	if ev := recvEvent(t, sub); ev.Type != ctrlproto.EventNotice {
		t.Fatalf("clear should broadcast a notice, got %q", ev.Type)
	}

	// A running turn blocks it.
	s.mu.Lock()
	s.turnCancel = func() {}
	s.mu.Unlock()
	if err := s.clear(); !errors.Is(err, ctrlproto.ErrBusy) {
		t.Fatalf("clear while busy should be ErrBusy, got %v", err)
	}
}

// TestWorkspaceTrust covers the trust verbs: Trust persists the verdict to the
// (temp-home) trust store, flips every open session's trust flag live, reflects
// it on SessionInfo, and broadcasts a session_updated; Untrust is the inverse.
func TestWorkspaceTrust(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TERVA_HOME", dir)
	sess, err := core.NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close() // release the file handle so TempDir cleanup works on Windows
	// A real agent so setTrusted's rebuildTools is safe even if Resolve succeeds;
	// nil extMgr takes the rebuildTools branch.
	s := &wsSession{id: "x", hub: newWSHub(), sess: sess, agent: core.NewAgent(nil, "fake", "", core.Registry{})}
	w := &Workspace{cwd: dir, args: Args{CWD: dir}, sessions: map[string]*wsSession{"x": s}}
	sub := s.hub.add(nil)

	if err := w.Trust(context.Background(), false); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	if !s.trusted.Load() {
		t.Fatal("session should be trusted after Trust")
	}
	if !s.info().Trusted {
		t.Fatal("SessionInfo.Trusted should reflect the grant")
	}
	if !resolveTrustState(w.args).IsTrusted() {
		t.Fatal("Trust should persist to the trust store")
	}
	if ev := recvEvent(t, sub); ev.Type != ctrlproto.EventSessionUpdated {
		t.Fatalf("Trust should broadcast session_updated, got %q", ev.Type)
	}

	if err := w.Untrust(context.Background()); err != nil {
		t.Fatalf("Untrust: %v", err)
	}
	if s.trusted.Load() {
		t.Fatal("session should be untrusted after Untrust")
	}
	if resolveTrustState(w.args).IsTrusted() {
		t.Fatal("Untrust should drop the workspace from the trust store")
	}
}

// TestCreateSessionPersonaValidation covers the CreateOpts.Persona honesty fix:
// an unknown persona is rejected up front with CodeBadRequest rather than being
// silently dropped (or surfacing as a CodeInternal from the deferred Resolve).
func TestCreateSessionPersonaValidation(t *testing.T) {
	t.Setenv("TERVA_HOME", t.TempDir())
	w := &Workspace{}
	_, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Persona: "no-such-persona-zzz"})
	if err == nil {
		t.Fatal("CreateSession with an unknown persona should error")
	}
	var ce *ctrlproto.Error
	if !errors.As(err, &ce) || ce.Code != ctrlproto.CodeBadRequest {
		t.Fatalf("want CodeBadRequest, got %v", err)
	}
}

// TestValidSessionID covers the path-traversal guard on wire session ids: a bare
// stem is fine, anything with a separator / ".." / that isn't its own basename
// is rejected (it would otherwise escape the sessions dir via filepath.Join).
func TestValidSessionID(t *testing.T) {
	good := []string{"abc", "2026-07-04T12-00-00", "session_1", "a.b"}
	for _, g := range good {
		if !validSessionID(g) {
			t.Errorf("valid id %q rejected", g)
		}
	}
	bad := []string{"", ".", "..", "../x", "a/b", `a\b`, `..\x`, "/etc/passwd", "a/../b", "sub/dir"}
	for _, b := range bad {
		if validSessionID(b) {
			t.Errorf("unsafe id %q accepted", b)
		}
	}
}

// TestSessionIDTraversalRejected proves the FS-touching wire methods refuse a
// traversal id before any filesystem access (guard fires ahead of sessionPath).
func TestSessionIDTraversalRejected(t *testing.T) {
	w := &Workspace{}
	for _, id := range []string{"../x", "a/b", "..", ""} {
		if err := w.DeleteSession(context.Background(), id); !errors.Is(err, ctrlproto.ErrNoSession) {
			t.Errorf("DeleteSession(%q) = %v, want ErrNoSession", id, err)
		}
		if err := w.RenameSession(context.Background(), id, "x"); !errors.Is(err, ctrlproto.ErrNoSession) {
			t.Errorf("RenameSession(%q) = %v, want ErrNoSession", id, err)
		}
		if _, err := w.Usage(context.Background(), id); !errors.Is(err, ctrlproto.ErrNoSession) {
			t.Errorf("Usage(%q) = %v, want ErrNoSession", id, err)
		}
	}
}

// hasSurfaceKind reports whether metas contains id with the given kind.
func hasSurfaceKind(metas []ctrlproto.SurfaceMeta, id, kind string) bool {
	for _, m := range metas {
		if m.ID == id {
			return m.Kind == kind
		}
	}
	return false
}

// TestWorkspaceCatalog covers serving the web string catalog to the browser:
// the effective (embedded ⊕ overlay) translations for a language, English-as-
// key. Restores English on return (Catalog re-Configures the process i18n).
func TestWorkspaceCatalog(t *testing.T) {
	defer i18n.Configure("en", "")
	w := &Workspace{}
	cv, err := w.Catalog(context.Background(), "fi")
	if err != nil {
		t.Fatalf("Catalog(fi): %v", err)
	}
	if cv.Lang != "fi" {
		t.Errorf("lang = %q, want fi", cv.Lang)
	}
	if cv.Singular["Message terva…"] != "Viesti tervalle…" {
		t.Errorf("web fi catalog not served: %q", cv.Singular["Message terva…"])
	}
	if cv.Plural["%d tool call|%d tool calls"]["other"] == "" {
		t.Error("web fi plural not served")
	}
}

// TestSettingsLanguage covers the language switcher: the settings row surfaces
// the active language + available options, and setting it swaps the process
// locale live and persists it. Mutates global i18n + config, so it uses a temp
// home and restores English on return.
func TestSettingsLanguage(t *testing.T) {
	t.Setenv("TERVA_HOME", t.TempDir())
	defer i18n.Configure("en", "")

	s := newTestSession()
	s.ws = &Workspace{} // broadcastAll over an empty session set is a no-op

	sv := s.settingsView()
	var lang *ctrlproto.SettingItem
	for i := range sv.Items {
		if sv.Items[i].Key == "language" {
			lang = &sv.Items[i]
		}
	}
	if lang == nil || lang.Type != "enum" {
		t.Fatalf("no language enum row: %+v", sv.Items)
	}
	if lang.Value != "en" {
		t.Errorf("language value = %q, want en", lang.Value)
	}
	hasFi := false
	for _, o := range lang.Options {
		if o.Value == "fi" {
			hasFi = true
		}
	}
	if !hasFi {
		t.Errorf("language options should include the embedded fi: %+v", lang.Options)
	}

	if err := s.settingsAction("set", map[string]string{"key": "language", "value": "fi"}); err != nil {
		t.Fatalf("set language fi: %v", err)
	}
	if i18n.ActiveLang() != "fi" {
		t.Errorf("active language = %q, want fi (should switch live)", i18n.ActiveLang())
	}
	if cfg, _ := LoadConfig(); cfg.Language != "fi" {
		t.Errorf("config.language = %q, want fi (should persist)", cfg.Language)
	}
	if err := s.settingsAction("set", map[string]string{"key": "language", "value": "zz-not-a-lang"}); err == nil {
		t.Error("unknown language should error")
	}
}

// TestExtPanelSurface covers the extension-panel bridge: an opened panel joins
// the registry, broadcasts, is fetchable, and leaves on close.
func TestExtPanelSurface(t *testing.T) {
	s := &wsSession{id: "x", hub: newWSHub(), agent: core.NewAgent(nil, "fake", "", core.Registry{}), extPanels: map[string]*webPanel{}}
	sub := s.hub.add(nil)

	s.paneOpen("memory", extproto.PanelSpec{ID: "main", Title: "Memory", Lines: []string{"a", "b"}})
	if ev := recvEvent(t, sub); ev.Type != ctrlproto.EventSurfacesChanged {
		t.Fatalf("want surfaces_changed, got %q", ev.Type)
	}
	if ev := recvEvent(t, sub); ev.Type != ctrlproto.EventSurfaceUpdated || ev.SurfaceID != "ext:memory:main" {
		t.Fatalf("want surface_updated ext:memory:main, got %+v", ev)
	}

	if !hasSurface(s.surfaceList(), "ext:memory:main") {
		t.Fatal("panel not in registry")
	}
	sf, err := s.surface("ext:memory:main")
	if err != nil || sf.Kind != "panel" || sf.Panel == nil || len(sf.Panel.Lines) != 2 {
		t.Fatalf("panel surface: %+v err=%v", sf, err)
	}

	s.paneClose("memory", "main")
	if hasSurface(s.surfaceList(), "ext:memory:main") {
		t.Error("panel still present after close")
	}
}

// TestTasksSurface covers the tasks pane against an empty workspace-global
// swarm: an empty list, a well-formed surface, and action validation.
func TestTasksSurface(t *testing.T) {
	tmp := t.TempDir()
	w := &Workspace{root: tmp, cwd: tmp, sessions: map[string]*wsSession{}, swarm: swarm.New(swarm.Config{Root: tmp, RepoRoot: tmp})}
	if tl := w.taskList(); tl == nil || len(tl.Tasks) != 0 {
		t.Fatalf("empty swarm should yield empty task list, got %+v", tl)
	}
	if err := w.taskAction("bogus", nil); err == nil {
		t.Error("unknown tasks action should error")
	}
	if err := w.taskAction("stop", map[string]string{"id": "nope"}); err == nil {
		t.Error("stopping a missing agent should error")
	}
	s := &wsSession{id: "x", ws: w, hub: newWSHub(), agent: core.NewAgent(nil, "fake", "", core.Registry{}), extPanels: map[string]*webPanel{}}
	sf, err := s.surface("tasks")
	if err != nil || sf.Kind != "tasks" || sf.Tasks == nil {
		t.Fatalf("tasks surface: %+v err=%v", sf, err)
	}
}

// TestSettingsSurface covers the settings pane: the view reflects the gate's
// approval mode, and a "set approval" action swaps it live (per-session, no
// config write). Reasoning/auto_title write real config, so are not exercised.
func TestSettingsSurface(t *testing.T) {
	pol, _ := buildPermissionPolicy(Args{Mode: ModeWeb})
	if pol == nil {
		t.Skip("no policy for web mode")
	}
	gate := core.NewPolicyGate(pol, nil)
	w := &Workspace{sessions: map[string]*wsSession{}}
	s := &wsSession{id: "x", ws: w, hub: newWSHub(), gate: gate, agent: core.NewAgent(nil, "fake", "", core.Registry{}), extPanels: map[string]*webPanel{}}

	v := s.settingsView()
	if settingValue(v, "approval") != string(core.ApprovalWorkspace) {
		t.Errorf("approval value = %q, want workspace", settingValue(v, "approval"))
	}
	if settingValue(v, "reasoning") == "<missing>" || settingValue(v, "auto_title") == "<missing>" {
		t.Errorf("settings view missing rows: %+v", v.Items)
	}

	if err := s.settingsAction("set", map[string]string{"key": "approval", "value": "plan"}); err != nil {
		t.Fatalf("set approval: %v", err)
	}
	if gate.Mode() != core.ApprovalPlan {
		t.Errorf("gate mode = %v, want plan", gate.Mode())
	}
	if err := s.settingsAction("set", map[string]string{"key": "nope"}); err == nil {
		t.Error("unknown setting should error")
	}
	if err := s.settingsAction("frob", nil); err == nil {
		t.Error("unknown action should error")
	}

	sf, err := s.surface("settings")
	if err != nil || sf.Kind != "settings" || sf.Settings == nil {
		t.Fatalf("settings surface: %+v err=%v", sf, err)
	}
}

func settingValue(v ctrlproto.SettingsView, key string) string {
	for _, it := range v.Items {
		if it.Key == key {
			return it.Value
		}
	}
	return "<missing>"
}

func hasSurface(metas []ctrlproto.SurfaceMeta, id string) bool {
	for _, m := range metas {
		if m.ID == id {
			return true
		}
	}
	return false
}

func titleOf(list []ctrlproto.SessionInfo, id string) string {
	for _, s := range list {
		if s.ID == id {
			return s.Title
		}
	}
	return "<not found>"
}
