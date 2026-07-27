package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/agent/modes"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/relaunch"
	"terva.sh/terva/packages/testsupport"
)

func newTestSession() *wsSession {
	return &wsSession{
		id:      "test",
		hub:     newWSHub(),
		permReq: map[string]ctrlproto.PermissionRequest{},
		askReq:  map[string]ctrlproto.AskRequest{},
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
	a := h.add(func() ctrlproto.Event { return snap }, false)
	b := h.add(nil, false)

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

// TestWSHubReliableDeliversAllUnderBackpressure: a reliable subscriber (the
// in-process TUI carrier) must receive every event in order even when the
// broadcaster far outruns it — a dropped text delta corrupts the transcript.
// Broadcasting far more than the buffer forces the no-drop send to block until
// the consumer drains, which is exactly the backpressure we want.
func TestWSHubReliableDeliversAllUnderBackpressure(t *testing.T) {
	h := newWSHub()
	reliable := h.add(nil, true)
	const n = hubBuffer * 3 // far exceeds the buffer

	go func() {
		for i := range n {
			h.broadcast(ctrlproto.ConversationEvent(core.WireEvent{Type: "text_delta", Delta: strconv.Itoa(i)}))
		}
	}()

	for i := range n {
		ev := recvEvent(t, reliable)
		if ev.Delta != strconv.Itoa(i) {
			t.Fatalf("event %d dropped or reordered: got delta %q", i, ev.Delta)
		}
	}
}

// TestWSHubLossyDropsUnderBackpressure: a lossy subscriber (a networked carrier)
// never blocks the broadcaster — past the buffer it drops, so the turn keeps
// moving for everyone else. This is the behavior the reliable path deliberately
// diverges from.
func TestWSHubLossyDropsUnderBackpressure(t *testing.T) {
	h := newWSHub()
	lossy := h.add(nil, false)
	const n = hubBuffer * 2

	// No draining: a lossy broadcast must not block despite the full buffer.
	for i := range n {
		h.broadcast(ctrlproto.ConversationEvent(core.WireEvent{Type: "text_delta", Delta: strconv.Itoa(i)}))
	}

	got := 0
	for draining := true; draining; {
		select {
		case <-lossy:
			got++
		default:
			draining = false
		}
	}
	if got == 0 {
		t.Fatal("lossy subscriber kept nothing")
	}
	if got >= n {
		t.Fatalf("lossy subscriber kept all %d events; expected drops past the %d buffer", n, hubBuffer)
	}
}

// TestWorkspaceDiagRedirect guards the invariant that host session-build
// diagnostics can be steered off os.Stderr — a stray write corrupts the
// in-process TUI's full-screen UI. SetDiag redirects; SetDiag(nil) silences;
// a session with no workspace never panics.
func TestWorkspaceDiagRedirect(t *testing.T) {
	var got []string
	w := &Workspace{diag: func(string) {}}
	w.SetDiag(func(m string) { got = append(got, m) })
	s := &wsSession{ws: w}

	s.diag("extension load: boom")
	if len(got) != 1 || got[0] != "extension load: boom" {
		t.Fatalf("diag not redirected: %v", got)
	}

	w.SetDiag(nil) // silence
	s.diag("should vanish")
	if len(got) != 1 {
		t.Fatalf("SetDiag(nil) should silence, got %v", got)
	}

	// A session built outside a Workspace (test fixtures) must not panic.
	(&wsSession{}).diag("no workspace")
}

func TestWebConfirmerApproveWins(t *testing.T) {
	s := newTestSession()
	sub := s.hub.add(nil, false)
	s.mu.Lock()
	s.turnCtx = t.Context()
	s.mu.Unlock()

	result := make(chan core.ConfirmDecision, 1)
	go func() { result <- (&webConfirmer{s: s}).ConfirmWithCall("bash", "ls -la", "call_42") }()

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
// TestSnapshotCarriesImageData: the snapshot builder uses the FULL wire
// form, so a client rendering from snapshots (the TUI carrier, a
// reconnecting web tab) gets real pixels, not just sizes. Serialized
// carriers strip at their connection boundary per negotiation (covered in
// ctrlproto's strip tests).
func TestSnapshotCarriesImageData(t *testing.T) {
	tmp := testsupport.TempDir(t)
	sess, err := core.NewSession(tmp, tmp, "p", "m", "test")
	if err != nil {
		t.Fatal(err)
	}
	s := newTestSession()
	s.sess = sess
	s.agent = core.NewAgent(nil, "m", "", core.Registry{})
	s.agent.SetMessages([]provider.Message{{
		Role: provider.RoleUser,
		Content: []provider.Content{
			provider.TextBlock{Text: "look at this"},
			provider.ImageBlock{MimeType: "image/png", Data: []byte{1, 2, 3}},
		},
	}})
	snap := s.snapshot()
	if len(snap.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(snap.Messages))
	}
	img := snap.Messages[0].Content[1]
	if string(img.Data) != "\x01\x02\x03" || img.Bytes != 3 || img.MimeType != "image/png" {
		t.Fatalf("snapshot image block = %+v, want the full form", img)
	}
}

// re-surface the dialog) and cleared once resolved.
func TestPendingPermissionRecordedForSnapshot(t *testing.T) {
	s := newTestSession()
	s.mu.Lock()
	s.turnCtx = t.Context()
	s.mu.Unlock()
	sub := s.hub.add(nil, false)

	go (&webConfirmer{s: s}).ConfirmWithCall("bash", "ls -la", "c9")

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
	s.mu.Unlock()

	result := make(chan core.ConfirmDecision, 1)
	go func() { result <- (&webConfirmer{s: s}).ConfirmWithCall("bash", "rm -rf /", "c1") }()
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
	sub := s.hub.add(nil, false)

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
	tmp := testsupport.TempDir(t)
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

	id1 := build.SessionIDFromPath(s1.Path)
	id2 := build.SessionIDFromPath(s2.Path)

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
	tmp := testsupport.TempDir(t)
	w := &Workspace{root: tmp, cwd: tmp, version: "test", sessions: map[string]*wsSession{}}
	ctx := context.Background()

	s, err := core.NewSession(tmp, tmp, "p", "m", "test") // empty + fresh
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	id := build.SessionIDFromPath(s.Path)
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

// Deleting a session removes its error sidecar too — the sidecar is that
// session's data, and because sidecars are filtered from session listings an
// orphan would be invisible and never cleaned up.
func TestDeleteSessionRemovesErrorSidecar(t *testing.T) {
	tmp := testsupport.TempDir(t)
	w := &Workspace{root: tmp, cwd: tmp, version: "test", sessions: map[string]*wsSession{}}
	ctx := context.Background()

	s, err := core.NewSession(tmp, tmp, "p", "m", "test")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	msg := provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}}
	_ = s.AppendMessage(msg)
	if err := s.LogError("provider exploded"); err != nil {
		t.Fatalf("LogError: %v", err)
	}
	sidecar := s.ErrorLogPath()
	_ = s.Close()

	if err := w.DeleteSession(ctx, build.SessionIDFromPath(s.Path)); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Error("transcript still present after delete")
	}
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Error("error sidecar still present after delete")
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
	tmp := testsupport.TempDir(t)
	w := &Workspace{root: tmp, cwd: tmp, version: "test", sessions: map[string]*wsSession{}}
	sess, err := core.NewSession(tmp, tmp, "p", "m", "test")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	_ = sess.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "help me refactor the parser"}}})
	s := &wsSession{id: build.SessionIDFromPath(sess.Path), ws: w, sess: sess, hub: newWSHub()}

	sub := s.hub.add(nil, false)
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
	sub := s.hub.add(nil, false)
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
	sub := s.hub.add(nil, false)

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

// gatedTurnClient is a provider.Client whose stream parks until the test
// releases it, so a test can act (queue, cancel) at a known point mid-turn.
// started signals each Stream call; fail ends the turn with a non-retryable
// error instead of a reply.
type gatedTurnClient struct {
	started chan struct{}
	release chan struct{}
	fail    bool
}

func (c *gatedTurnClient) Name() string { return "gated-fake" }

func (c *gatedTurnClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	select {
	case c.started <- struct{}{}:
	default:
	}
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		<-c.release
		if c.fail {
			out <- provider.EventDone{Stop: provider.StopError, Err: errors.New("boom: bad request")}
			return
		}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "ok"}},
		}}
	}()
	return out, nil
}

// newTurnTestSession builds the minimal wsSession that can run real turns:
// a live agent with the buildSession OnEvent fan-out, inside a workspace shell
// with a background context. Title pre-set so settleTitle short-circuits (no
// session file to rename).
func newTurnTestSession(t *testing.T, cl provider.Client) *wsSession {
	t.Helper()
	tmp := testsupport.TempDir(t)
	// A real session file backs the harness: production sessions always have
	// one, and the turn path's snapshot-on-done re-broadcast reads its
	// path/meta through info().
	sess, err := core.NewSession(tmp, tmp, "p", "fake-model", "test")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	s := &wsSession{
		id:    "turns",
		ws:    &Workspace{ctx: context.Background(), diag: func(string) {}},
		hub:   newWSHub(),
		sess:  sess,
		agent: core.NewAgent(cl, "fake-model", "", core.Registry{}),
		title: "titled",
	}
	s.agent.AddEventObserver(func(ev core.AgentEvent) {
		s.broadcast(ctrlproto.ConversationEvent(core.EventToWire(ev)))
	})
	return s
}

// TestWorkspaceSnapshotRebroadcastOnTurnEnd: a subscriber that attaches
// mid-turn gets a snapshot that predates the turn's seal (agent.Messages()
// lags the live per-tool events, and the hub cannot replay events from before
// the subscriber existed), so the daemon must re-broadcast the authoritative
// snapshot at turn end — the compact/clear resync mechanism extended to done.
func TestWorkspaceSnapshotRebroadcastOnTurnEnd(t *testing.T) {
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	s := newTurnTestSession(t, cl)

	if err := s.prompt("hi", nil); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	<-cl.started
	// Attach mid-turn the way s.subscribe does: snapshot enqueued first.
	sub := s.hub.add(func() ctrlproto.Event { return ctrlproto.SnapshotEvent(s.snapshot()) }, true)
	first := recvEvent(t, sub)
	if first.Type != ctrlproto.EventSnapshot || first.Snapshot == nil {
		t.Fatalf("want initial snapshot first, got %q", first.Type)
	}
	if got := len(first.Snapshot.Messages); got != 1 {
		t.Fatalf("mid-turn snapshot should hold just the user message, got %d", got)
	}
	close(cl.release)
	drainUntil(t, sub, "done")
	resync, _ := drainUntil(t, sub, ctrlproto.EventSnapshot)
	if resync.Snapshot == nil || len(resync.Snapshot.Messages) != 2 {
		t.Fatalf("turn-end snapshot should carry the sealed transcript, got %+v", resync.Snapshot)
	}
	if txt := wireMessageText(&resync.Snapshot.Messages[1]); txt != "ok" {
		t.Fatalf("sealed assistant message = %q, want %q", txt, "ok")
	}
}

// TestWorkspaceSetApprovalPlanWithholdsTools: the daemon settings surface's
// approval switch must reshape the model's tool VIEW, not just the confirm
// gate — plan mode withholds mutating built-ins from the live agent, and
// switching back restores them (parity with the legacy /permissions switch,
// cli.go setApprovalMode). The web client rides the same verb.
func TestWorkspaceSetApprovalPlanWithholdsTools(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	args := build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true}
	r, err := build.Resolve(args, true)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
	s := &wsSession{id: "approval", ws: w, hub: newWSHub(), args: args}
	w.sessions[s.id] = s // settingsAction's surface refresh rides BroadcastAll
	extMgr, stopExt := setupWebExtensions(w.ctx, args, &r, "test", s)
	s.extMgr = extMgr
	defer stopExt()
	s.agent = core.NewAgent(&gatedTurnClient{}, r.Model, r.SystemPrompt, r.ToolRegistry)

	if _, ok := s.agent.LookupTool("write"); !ok {
		t.Fatal("baseline registry should include the write tool")
	}
	sub := s.hub.add(nil, true)
	// The settings surface is workspace-scoped, so its refresh is published to
	// the workspace, not stamped onto each live session's stream. The notice is a
	// fact about THIS session's agent being rebuilt, and still rides the session.
	wsEvents := w.events().add(nil, true)

	if err := s.settingsAction("set", map[string]string{"key": "approval", "value": "plan"}); err != nil {
		t.Fatalf("set approval plan: %v", err)
	}
	if _, ok := s.agent.LookupTool("write"); ok {
		t.Error("plan mode must withhold the write tool from the model's view")
	}
	if _, ok := s.agent.LookupTool("read"); !ok {
		t.Error("plan mode must keep read-only tools")
	}
	// The cache-breaking rebuild announces itself: a kinded notice with the
	// trigger and scope, before the settings surface refresh.
	ev, _ := drainUntil(t, sub, ctrlproto.EventNotice)
	if ev.Notice == nil || ev.Notice.Kind != ctrlproto.NoticePromptRebuilt {
		t.Fatalf("want a %s notice, got %+v", ctrlproto.NoticePromptRebuilt, ev.Notice)
	}
	if ev.Notice.Data["reason"] != "approval-mode" || ev.Notice.Data["scope"] == "" {
		t.Errorf("notice data = %+v; want reason approval-mode with a scope", ev.Notice.Data)
	}
	drainUntil(t, wsEvents, ctrlproto.EventSurfaceUpdated)

	// Re-applying the same mode rebuilds an identical view — no cache break,
	// so no notice: only the surface refresh goes out.
	if err := s.settingsAction("set", map[string]string{"key": "approval", "value": "plan"}); err != nil {
		t.Fatalf("re-set approval plan: %v", err)
	}
	// The surface refresh is the sentinel that the action finished. Both
	// broadcasts are synchronous and in order (notice, then surface), so once the
	// surface event lands, any notice would already be sitting on the session
	// stream — and there must not be one.
	drainUntil(t, wsEvents, ctrlproto.EventSurfaceUpdated)
	select {
	case e := <-sub:
		t.Errorf("identical rebuild must not emit a notice, got %+v", e.Notice)
	default:
	}

	if err := s.settingsAction("set", map[string]string{"key": "approval", "value": "workspace"}); err != nil {
		t.Fatalf("set approval workspace: %v", err)
	}
	if _, ok := s.agent.LookupTool("write"); !ok {
		t.Error("leaving plan mode must restore mutating tools")
	}
}

// drainUntil consumes events until one of the wanted types arrives, returning
// it plus every event seen on the way (for ordering assertions).
func drainUntil(t *testing.T, ch <-chan ctrlproto.Event, want ...string) (ctrlproto.Event, []ctrlproto.Event) {
	t.Helper()
	var seen []ctrlproto.Event
	for {
		ev := recvEvent(t, ch)
		seen = append(seen, ev)
		if slices.Contains(want, ev.Type) {
			return ev, seen
		}
	}
}

// waitIdle blocks until the turn slot is released, because a "done" event does
// NOT mean the session is idle yet.
//
// On the clean path launchTurn runs `err := gen(turnCtx)` and only then calls
// endTurn, while the agent broadcasts its own "done" from INSIDE gen — so a
// subscriber always observes "done" strictly before endTurn clears turnCancel
// (which is all busy() reads). The error path has the same shape, just narrower:
// an explicit done broadcast immediately precedes the endTurn call.
//
// So `drainUntil(t, sub, "done"); if s.busy() {…}` is racy by construction. It
// passes on a quiet machine and loses under `just ci`, whose whole-tree
// `go test -race ./...` widens the window — a flake that reads as "the branch I
// am rebasing broke this" to whoever hits it next.
//
// The ordering itself is deliberate and must not be "fixed" by releasing the
// slot before the broadcast: endTurn settles the pending queue under the same
// s.mu hold, and an earlier release would reopen the gap its doc comment exists
// to close. The event means "the turn's output is complete", not "the engine is
// ready for the next one" — so a test that wants the latter waits for it.
func waitIdle(t *testing.T, s *wsSession) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !s.busy() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Error("turn slot was never released")
}

// TestWorkspaceTurnErrorEmitsErrorThenDone: the agent's own "done" does not
// fire when the run loop returns an error, so the workspace must synthesize a
// definitive completion — error first (status/rescue payload), then done (the
// busy-clearing signal) — or every stream consumer's busy state sticks.
func TestWorkspaceTurnErrorEmitsErrorThenDone(t *testing.T) {
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{}), fail: true}
	s := newTurnTestSession(t, cl)
	sub := s.hub.add(nil, true)

	if err := s.prompt("hi", nil); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	<-cl.started
	// Queue a follow-up mid-turn: the failed turn must drop it (stale
	// follow-ups must not fire after a failure), broadcasting the empty queue.
	s.queue("stale follow-up")
	close(cl.release)

	done, seen := drainUntil(t, sub, "done")
	_ = done
	var errAt, doneAt = -1, -1
	for i, ev := range seen {
		switch ev.Type {
		case "error":
			errAt = i
		case "done":
			doneAt = i
		}
	}
	if errAt < 0 || doneAt < 0 || errAt > doneAt {
		t.Fatalf("want error before done, got order %v", eventTypes(seen))
	}
	// The dropped queue converges every client on empty.
	qe, _ := drainUntil(t, sub, ctrlproto.EventQueueUpdated)
	if len(qe.Queued) != 0 {
		t.Fatalf("failed turn should drop the queue, got %v", qe.Queued)
	}
	if n := s.agent.QueuedMessageCount(); n != 0 {
		t.Fatalf("queued count after failed turn = %d, want 0", n)
	}
}

// TestWorkspaceTurnErrorPersistsToSidecar: a failed turn's error is transient
// on the header but durable in the session's error sidecar, so a red X is
// recoverable after the fact — and the transcript itself stays clean.
func TestWorkspaceTurnErrorPersistsToSidecar(t *testing.T) {
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{}), fail: true}
	s := newTurnTestSession(t, cl)
	sub := s.hub.add(nil, true)

	if err := s.prompt("hi", nil); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	<-cl.started
	close(cl.release)
	drainUntil(t, sub, "done")

	sidecar, err := os.ReadFile(s.sess.ErrorLogPath())
	if err != nil {
		t.Fatalf("error sidecar not written: %v", err)
	}
	if !strings.Contains(string(sidecar), "\"error\"") {
		t.Errorf("sidecar has no error row: %s", sidecar)
	}
	// The transcript must not have gained an error row.
	transcript, err := os.ReadFile(s.sess.Path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	if strings.Contains(string(transcript), "\"type\":\"error\"") {
		t.Errorf("error leaked into the transcript: %s", transcript)
	}
}

// TestWorkspaceQueueRestartsAfterTurn: a message queued in the turn's final
// instants (after the run loop's last boundary check) must not strand — the
// workspace shifts the queue head and starts the next turn itself, the
// daemon-side mirror of the TUI turn engine's release semantics.
func TestWorkspaceQueueRestartsAfterTurn(t *testing.T) {
	cl := &gatedTurnClient{started: make(chan struct{}, 2), release: make(chan struct{}, 2)}
	s := newTurnTestSession(t, cl)
	sub := s.hub.add(nil, true)

	cl.release <- struct{}{}
	if err := s.prompt("first", nil); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	// The agent's "done" means the run loop is past its final queued-message
	// check; whichever side of endTurn this lands on, the message must still
	// run as its own turn.
	drainUntil(t, sub, "done")
	s.queue("second")
	cl.release <- struct{}{}

	ev, _ := drainUntil(t, sub, "user_message")
	if got := wireMessageText(ev.Message); got != "second" {
		t.Fatalf("restarted turn user message = %q, want %q", got, "second")
	}
	drainUntil(t, sub, "done")
	if n := s.agent.QueuedMessageCount(); n != 0 {
		t.Fatalf("queued count after restart = %d, want 0", n)
	}
}

// TestWorkspaceQueueWhileIdlePrompts: queueing with no turn running is a
// deferred Prompt (the interface contract's discretion) — it starts a turn
// instead of stranding until the next user prompt.
func TestWorkspaceQueueWhileIdlePrompts(t *testing.T) {
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{}, 1)}
	s := newTurnTestSession(t, cl)
	sub := s.hub.add(nil, true)

	cl.release <- struct{}{}
	s.queue("go")
	ev, _ := drainUntil(t, sub, "user_message")
	if got := wireMessageText(ev.Message); got != "go" {
		t.Fatalf("idle queue user message = %q, want %q", got, "go")
	}
	drainUntil(t, sub, "done")
	if n := s.agent.QueuedMessageCount(); n != 0 {
		t.Fatalf("queued count = %d, want 0", n)
	}
}

func eventTypes(evs []ctrlproto.Event) []string {
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = ev.Type
	}
	return out
}

func wireMessageText(m *core.WireMessage) string {
	if m == nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range m.Content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
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

// TestContextBreakdownLazyAdvertised: under lazy visibility the tool weight
// counts only the ADVERTISED set (what reaches the model), and the installed
// totals ride separate fields for the "N of M tools" split. A hidden group's
// schemas never inflate ToolBytes or TotalBytes.
func TestContextBreakdownLazyAdvertised(t *testing.T) {
	reg := core.Registry{
		"read":      ctxFakeTool{name: "read"},                   // core → advertised
		"mail_send": ctxFakeTool{name: "mail_send", ext: "mail"}, // hidden group
	}
	ag := core.NewAgent(nil, "fake", "", reg)
	ag.EnableLazyTools() // only core active; "mail" stays inactive
	s := &wsSession{id: "x", hub: newWSHub(), agent: ag}

	b := s.contextBreakdown()
	if b.ToolCount != 1 {
		t.Errorf("advertised tool count = %d, want 1 (core only)", b.ToolCount)
	}
	if b.ToolCountInstalled != 2 {
		t.Errorf("installed tool count = %d, want 2", b.ToolCountInstalled)
	}
	if b.ToolBytesInstalled <= b.ToolBytes {
		t.Errorf("installed bytes %d should exceed advertised bytes %d", b.ToolBytesInstalled, b.ToolBytes)
	}
	// TotalBytes counts only the advertised weight — the hidden schema is not in
	// the context window (only its name rides the ephemeral capability note).
	if b.TotalBytes != b.SystemBytes+b.ToolBytes+b.ExtBytes+b.TranscriptBytes {
		t.Errorf("total %d != sum of advertised parts", b.TotalBytes)
	}
	// The hidden group's names DO cost a few bytes on the ephemeral tail: the
	// capability note is captured and folded into the ephemeral (ext) total.
	if b.LazyNoteBytes <= 0 {
		t.Errorf("a hidden group should contribute a capability note; LazyNoteBytes = %d", b.LazyNoteBytes)
	}
	if b.ExtBytes < b.LazyNoteBytes {
		t.Errorf("ext bytes %d should include the lazy note %d", b.ExtBytes, b.LazyNoteBytes)
	}
	if !strings.Contains(ag.CapabilityNote(), "mail_send") {
		t.Errorf("capability note should name the hidden tool, got %q", ag.CapabilityNote())
	}
}

// TestContextTree covers the context-tree outline: sections at the root, the
// transcript grouped into turns by user-prompt boundary, with a leading
// compaction summary + preserved tail collected under "preserved context"
// rather than masquerading as turn #1. Message ids carry the global index.
func TestContextTree(t *testing.T) {
	ag := core.NewAgent(nil, "fake", "", core.Registry{})
	ag.System = "sys"
	ag.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Meta: map[string]string{"compaction": "true"},
			Content: []provider.Content{provider.TextBlock{Text: "## Context Summary (compacted)"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "kept tail"}}},
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "refactor the parser"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "done"}}},
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "now add tests"}}},
	})
	s := &wsSession{id: "x", hub: newWSHub(), agent: ag}

	b := s.contextBreakdown()
	if b.Tree == nil {
		t.Fatal("tree is nil")
	}
	if b.Rev == 0 {
		t.Error("rev should be non-zero after SetMessages")
	}
	if b.Tree.ID != "root" || len(b.Tree.Children) != 4 {
		t.Fatalf("root = %+v (want id=root with 4 sections)", b.Tree)
	}
	tr := b.Tree.Children[3]
	if tr.ID != "tr" || tr.Kind != "section" {
		t.Fatalf("4th section should be transcript, got %+v", tr)
	}
	if len(tr.Children) != 3 {
		t.Fatalf("transcript turns = %d, want 3 (preserved + 2 prompts)\n%+v", len(tr.Children), tr.Children)
	}
	pre, t1, t2 := tr.Children[0], tr.Children[1], tr.Children[2]
	if pre.Label != "preserved context" || len(pre.Children) != 2 {
		t.Errorf("leading group = %q with %d msgs, want 'preserved context' with 2", pre.Label, len(pre.Children))
	}
	if t1.Label != "turn #1" || t1.Summary != "refactor the parser" || len(t1.Children) != 2 {
		t.Errorf("turn 1 = %+v", t1)
	}
	if t1.Children[0].ID != "tr/m2" {
		t.Errorf("turn 1 first message id = %q, want tr/m2 (global index)", t1.Children[0].ID)
	}
	if t2.Label != "turn #2" || t2.Summary != "now add tests" || len(t2.Children) != 1 {
		t.Errorf("turn 2 = %+v", t2)
	}
}

// TestContextNode covers the lazy expand: a message id resolves to its content
// blocks (with bodies), the tools section to per-tool specs, and a stale/unknown
// id fails with not_found. Reveal ops are not served yet.
func TestContextNode(t *testing.T) {
	ag := core.NewAgent(nil, "fake", "SYSTEM PROMPT", core.Registry{})
	ag.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hello there"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.TextBlock{Text: "on it"},
			provider.ToolCallBlock{ID: "c1", Name: "bash", Arguments: []byte(`{"cmd":"ls"}`)},
		}},
	})
	s := &wsSession{id: "x", hub: newWSHub(), agent: ag}

	// A message expands to its content blocks, each carrying its body.
	n, err := s.contextNode("tr/m1", "")
	if err != nil {
		t.Fatalf("contextNode(tr/m1): %v", err)
	}
	if n.Kind != "message" || len(n.Children) != 2 {
		t.Fatalf("message node = %+v, want 2 block children", n)
	}
	if n.Children[0].Content != "on it" {
		t.Errorf("first block content = %q, want %q", n.Children[0].Content, "on it")
	}
	if n.Children[1].Kind != "block" || !strings.Contains(n.Children[1].Content, "bash") {
		t.Errorf("tool-call block = %+v, want content mentioning bash", n.Children[1])
	}

	// The system prompt expands to its text.
	if sys, err := s.contextNode("sys", ""); err != nil || sys.Content != "SYSTEM PROMPT" {
		t.Errorf("sys node = %+v err=%v", sys, err)
	}

	// A stale/unknown id is not_found; an unknown op is unsupported.
	if _, err := s.contextNode("tr/m9", ""); err == nil {
		t.Error("out-of-range message id should error")
	}
	if _, err := s.contextNode("tr/m0", "bogus"); err == nil {
		t.Error("unknown op should be unsupported")
	}
}

// TestRevealCompactionNode drives the reveal op end-to-end over a persisted,
// twice-compacted session: the live summary is marked revealable, revealing it
// returns the span it replaced with the prior summary as a nested revealable
// node, and revealing that walks one epoch further back.
func TestRevealCompactionNode(t *testing.T) {
	dir := testsupport.TempDir(t)
	sess, err := core.NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	um := func(s string) provider.Message {
		return provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: s}}}
	}
	sm := func(s string) provider.Message {
		m := um(s)
		m.Meta = map[string]string{"compaction": "true"}
		return m
	}
	for _, s := range []string{"m0", "m1", "m2", "m3"} {
		_ = sess.AppendMessage(um(s))
	}
	_ = sess.AppendCompaction([]provider.Message{sm("summary1"), um("m2"), um("m3")}, core.CompactResult{})
	_ = sess.AppendMessage(um("m4"))
	_ = sess.AppendMessage(um("m5"))
	_ = sess.AppendCompaction([]provider.Message{sm("summary2"), um("m4"), um("m5")}, core.CompactResult{})

	// The live agent after compaction holds the latest summary + its kept tail.
	ag := core.NewAgent(nil, "fake", "", core.Registry{})
	ag.SetMessages([]provider.Message{
		sm("summary2"),
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "m4"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "m5"}}},
	})
	s := &wsSession{id: "x", hub: newWSHub(), agent: ag, sess: sess}

	// Outline: the live summary (index 0) reads as a revealable event.
	tr := s.contextBreakdown().Tree.Children[3]
	summaryNode := tr.Children[0].Children[0]
	if summaryNode.Kind != "event" || summaryNode.Reveal != "compaction" {
		t.Fatalf("live summary node = %+v, want kind=event reveal=compaction", summaryNode)
	}

	// Reveal it → [summary1, m2, m3]; summary1 is a nested reveal to the prior epoch.
	n, err := s.contextNode("tr/m0", "compaction")
	if err != nil {
		t.Fatalf("reveal(tr/m0): %v", err)
	}
	if n.Kind != "event" || len(n.Children) != 3 {
		t.Fatalf("reveal node = %+v, want 3 replaced children", n)
	}
	if n.Children[0].ID != "ev/c0" || n.Children[0].Reveal != "compaction" {
		t.Errorf("first revealed child should be the prior summary reveal: %+v", n.Children[0])
	}

	// Reveal the prior epoch → [m0, m1].
	prev, err := s.contextNode("ev/c0", "compaction")
	if err != nil {
		t.Fatalf("reveal(ev/c0): %v", err)
	}
	if len(prev.Children) != 2 {
		t.Errorf("prior reveal = %d children, want 2 (m0, m1)", len(prev.Children))
	}

	// A live-only session (no durable file) can't reveal.
	nolog := &wsSession{id: "y", hub: newWSHub(), agent: ag}
	if _, err := nolog.contextNode("tr/m0", "compaction"); err == nil {
		t.Error("reveal without a persisted session should error")
	}
}

// TestExtContextItems covers the stage-4 facet: an extension's structured
// context snapshot maps to per-source leaf nodes, split by kind (static guidance
// vs ephemeral cards) with ids namespaced under the section.
func TestExtContextItems(t *testing.T) {
	items := []extensions.ContextItem{
		{Source: "git-worktree", Kind: "static", Text: "worktree guidance"},
		{Source: "memory", Kind: "static", Text: "memory guidance"},
		{Source: "world", Kind: "card", ID: "scene1", Label: "Tavern", Text: "a cozy tavern"},
	}
	statics := ctxExtItems(items, "static", "sys/xg")
	if len(statics) != 2 || statics[0].ID != "sys/xg/git-worktree" || statics[0].Content != "worktree guidance" {
		t.Fatalf("static items = %+v", statics)
	}
	cards := ctxExtItems(items, "card", "xt/card")
	if len(cards) != 1 || cards[0].ID != "xt/card/world/scene1" || cards[0].Label != "world: Tavern" {
		t.Errorf("cards = %+v", cards)
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
	sub := s.hub.add(nil, false)

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

// TestWorkspaceRestartRefusalIsNonDestructive: a restart relaunch will REFUSE
// must not destroy anything on its way to the refusal. The drain cancels every
// in-flight turn, so it has to run AFTER the gate — otherwise an unsupported
// platform, a `go run`/debug binary, a failed exe capture or an already-pending
// restart costs the user a live turn for a restart that never happens.
//
// The refusal here is free and genuine: a `go test` binary lives in a go-build
// temp dir, which relaunch's preflight refuses (it cannot exec into itself). So
// enabling the capability puts us on exactly the path that used to drain first
// and refuse second.
func TestWorkspaceRestartRefusalIsNonDestructive(t *testing.T) {
	relaunch.Enable()
	t.Cleanup(relaunch.Disable)
	if err := relaunch.CanTrigger(); err == nil {
		t.Skip("relaunch would accept a restart from this binary; nothing to refuse")
	}

	w := &Workspace{sessions: map[string]*wsSession{}}
	s := &wsSession{}
	cancelled := false
	s.turnCancel = func() { cancelled = true }
	w.sessions["s1"] = s

	if err := w.Restart(context.Background()); err == nil {
		t.Fatal("Restart should refuse: relaunch cannot exec a go-build test binary")
	}
	if cancelled {
		t.Fatal("a REFUSED restart cancelled the in-flight turn — preflight must run before the drain")
	}
	if !s.busy() {
		t.Fatal("the in-flight turn should still be running after a refused restart")
	}
}

// TestCancelAndDrainTurnsWaitsForIdle pins the graceful half of the restart
// contract: the drain cancels every live turn and returns once they've reached
// endTurn (turnCancel cleared) — not before, so a cancelled turn gets its window
// to persist before the image is replaced.
func TestCancelAndDrainTurnsWaitsForIdle(t *testing.T) {
	w := &Workspace{sessions: map[string]*wsSession{}}
	s := &wsSession{}
	// Cancelling schedules the turn to reach endTurn (clear turnCancel) shortly
	// after, modeling a real turn unwinding and persisting.
	s.turnCancel = func() {
		go func() {
			time.Sleep(20 * time.Millisecond)
			s.mu.Lock()
			s.turnCancel = nil
			s.mu.Unlock()
		}()
	}
	w.sessions["s1"] = s

	start := time.Now()
	w.cancelAndDrainTurns(context.Background(), time.Second)
	if s.busy() {
		t.Fatal("drain returned while the turn was still busy")
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("drain took %v; it should return promptly once the turn goes idle", d)
	}
}

// TestCancelAndDrainTurnsBounded: a turn that ignores cancellation must not hang
// the restart — the drain cancels it, waits out its bounded budget, and returns.
func TestCancelAndDrainTurnsBounded(t *testing.T) {
	w := &Workspace{sessions: map[string]*wsSession{}}
	cancelled := false
	s := &wsSession{}
	s.turnCancel = func() { cancelled = true } // never clears turnCancel: stays busy
	w.sessions["s1"] = s

	start := time.Now()
	w.cancelAndDrainTurns(context.Background(), 50*time.Millisecond)
	if !cancelled {
		t.Fatal("drain must cancel the in-flight turn before waiting")
	}
	if d := time.Since(start); d < 40*time.Millisecond || d > time.Second {
		t.Fatalf("drain returned after %v; want ~the 50ms bounded budget", d)
	}
	if !s.busy() {
		t.Fatal("a turn ignoring cancellation should still read busy after the drain")
	}
}

// TestWorkspaceCompact covers user-driven compaction's two no-model-call paths:
// an empty transcript reports a benign notice (no error), and a running turn is
// refused with ErrBusy. The actual summarize+replace path needs a live model, so
// it's exercised by core's own compaction tests.
func TestWorkspaceCompact(t *testing.T) {
	s := &wsSession{id: "x", hub: newWSHub(), agent: core.NewAgent(nil, "fake", "", core.Registry{})}
	sub := s.hub.add(nil, false)

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

// TestExtensionsActionGlobalScope: the new manifest-scope toggle resolves the
// extension's install dir; an unknown name is a clean not-found, not an
// internal error (and never reaches the live-apply step).
func TestExtensionsActionGlobalScope(t *testing.T) {
	s := &wsSession{id: "x", cwd: testsupport.TempDir(t), hub: newWSHub()}
	err := s.extensionsAction("toggle", map[string]string{"name": "no-such-ext", "enabled": "true", "scope": "global"})
	var ce *ctrlproto.Error
	if !errors.As(err, &ce) || ce.Code != ctrlproto.CodeNotFound {
		t.Fatalf("global toggle of unknown ext = %v, want CodeNotFound", err)
	}
}

// TestSessionSummariesFromInfos: the wire→picker mapping keeps every field the
// /sessions picker renders; Title arrives pre-derived by the service
// (titleFromFirstText), so FirstUserText staying empty loses nothing.
func TestSessionSummariesFromInfos(t *testing.T) {
	got := SessionSummariesFromInfos([]ctrlproto.SessionInfo{{
		ID: "ab12", Title: "fix the parser", Provider: "openai", Model: "gpt-5.5",
		Path: "/s/ab12.jsonl", Created: "2026-07-04T10:00:00Z", Messages: 7,
		Usage: core.WireUsage{CostUSD: 1.25}, Live: true, Busy: true,
	}})
	if len(got) != 1 {
		t.Fatalf("got %d summaries, want 1", len(got))
	}
	s := got[0]
	if s.Path != "/s/ab12.jsonl" || s.Title != "fix the parser" || s.Provider != "openai" ||
		s.Model != "gpt-5.5" || s.MessageCount != 7 || s.TotalCost != 1.25 || s.Started.IsZero() {
		t.Fatalf("summary = %+v", s)
	}
	// The live flags ride through so the picker can glyph a busy/live row.
	if !s.Live || !s.Busy {
		t.Fatalf("live/busy not carried: Live=%v Busy=%v", s.Live, s.Busy)
	}
}

// TestTaskActionSpawnValidation: the tasks surface's spawn verb requires a
// swarm and a non-blank task, each a clean typed error — validation runs
// before the swarm is touched (a real spawn launches a child process).
func TestTaskActionSpawnValidation(t *testing.T) {
	w := &Workspace{}
	var ce *ctrlproto.Error
	if err := w.taskAction("spawn", map[string]string{"task": "x"}); !errors.As(err, &ce) || ce.Code != ctrlproto.CodeUnsupported {
		t.Fatalf("spawn without swarm = %v, want CodeUnsupported", err)
	}
	w.swarm = swarm.New(swarm.Config{Root: testsupport.TempDir(t), RepoRoot: testsupport.TempDir(t)})
	if err := w.taskAction("spawn", map[string]string{"task": "   "}); !errors.As(err, &ce) || ce.Code != ctrlproto.CodeBadRequest {
		t.Fatalf("spawn with blank task = %v, want CodeBadRequest", err)
	}
	// A foreign backend runs the human spawn through the SAME gate the model's
	// swarm_spawn tool uses; an unregistered name is refused before any spawn —
	// whichever way the external-workers knob is set (disabled → "disabled";
	// enabled → worker.Lookup's "unknown backend"). Either way, nothing launches.
	if err := w.taskAction("spawn", map[string]string{"task": "x", "backend": "nonesuch-backend"}); !errors.As(err, &ce) || ce.Code != ctrlproto.CodeBadRequest {
		t.Fatalf("spawn with an unknown backend = %v, want CodeBadRequest", err)
	}
	if n := len(w.swarm.SnapshotAll()); n != 0 {
		t.Fatalf("a refused backend must not spawn; swarm has %d agent(s)", n)
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
	sub := s.hub.add(nil, false)
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
	dir := testsupport.TempDir(t)
	s := &wsSession{id: "x", hub: newWSHub(), cwd: dir}

	if err := s.extensionsAction("bogus", map[string]string{"name": "foo"}); err == nil {
		t.Error("unknown action should error")
	}
	if err := s.extensionsAction("toggle", map[string]string{}); err == nil {
		t.Error("missing name should error")
	}

	sub := s.hub.add(nil, false)
	// Disable → foo lands in the project disable list.
	if err := s.extensionsAction("toggle", map[string]string{"name": "foo", "enabled": "false"}); err != nil {
		t.Fatalf("toggle off: %v", err)
	}
	if pc, err := config.LoadProjectConfig(dir); err != nil || pc == nil || !slices.Contains(pc.DisableExtensions, "foo") {
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
	if pc, _ := config.LoadProjectConfig(dir); pc != nil && slices.Contains(pc.DisableExtensions, "foo") {
		t.Errorf("foo should be re-enabled, still disabled: %+v", pc.DisableExtensions)
	}
}

// TestWorkspaceAutoSwarmToggleAppliesLive: the daemon settings surface's
// auto_swarm toggle must persist AND re-derive every live session's view —
// swarm_spawn joins/leaves the model's tools and the proactive-delegation
// nudge joins/leaves the system prompt on the toggle, not at the next
// session's construction. Both the carrier TUI and the web client ride this.
func TestWorkspaceAutoSwarmToggleAppliesLive(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	args := build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true}
	r, err := build.Resolve(args, true)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
	s := &wsSession{id: "swarm", ws: w, hub: newWSHub(), args: args}
	s.agent = core.NewAgent(&gatedTurnClient{}, r.Model, r.SystemPrompt, r.ToolRegistry)
	w.sessions[s.id] = s

	if _, ok := s.agent.LookupTool("swarm_spawn"); ok {
		t.Fatal("swarm_spawn should be absent while auto-swarm is off")
	}
	sub := s.hub.add(nil, true)
	if err := s.settingsAction("set", map[string]string{"key": "auto_swarm", "value": "true"}); err != nil {
		t.Fatalf("enable auto_swarm: %v", err)
	}
	if _, ok := s.agent.LookupTool("swarm_spawn"); !ok {
		t.Error("enabling auto-swarm must add swarm_spawn to the live tool set")
	}
	// The rebuild breaks the prompt cache and says so.
	if ev, _ := drainUntil(t, sub, ctrlproto.EventNotice); ev.Notice == nil ||
		ev.Notice.Kind != ctrlproto.NoticePromptRebuilt || ev.Notice.Data["reason"] != "auto-swarm" {
		t.Errorf("want a prompt_rebuilt notice with reason auto-swarm, got %+v", ev.Notice)
	}
	if !strings.Contains(s.agent.System, "swarm_spawn") {
		t.Error("enabling auto-swarm must add the nudge to the live system prompt")
	}
	if err := s.settingsAction("set", map[string]string{"key": "auto_swarm", "value": "false"}); err != nil {
		t.Fatalf("disable auto_swarm: %v", err)
	}
	if _, ok := s.agent.LookupTool("swarm_spawn"); ok {
		t.Error("disabling auto-swarm must remove swarm_spawn from the live tool set")
	}
	if strings.Contains(s.agent.System, "swarm_spawn") {
		t.Error("disabling auto-swarm must drop the nudge from the live system prompt")
	}
}

// TestExtensionsConfigAction: the extensions surface's "config" action is the
// daemon half of the config form (push the just-saved values to the running
// extension); it must be a valid verb that answers with a surface refresh.
// The live push itself needs a real subprocess, so extMgr is nil here
// (applyExtensionConfigLive no-ops), isolating the verb + broadcast.
func TestExtensionsConfigAction(t *testing.T) {
	s := &wsSession{id: "x", hub: newWSHub(), cwd: testsupport.TempDir(t)}
	sub := s.hub.add(nil, false)
	if err := s.extensionsAction("config", map[string]string{}); err == nil {
		t.Error("missing name should error")
	}
	if err := s.extensionsAction("config", map[string]string{"name": "foo"}); err != nil {
		t.Fatalf("config action: %v", err)
	}
	if ev := recvEvent(t, sub); ev.Type != ctrlproto.EventSurfaceUpdated || ev.SurfaceID != "extensions" {
		t.Errorf("want surface_updated(extensions), got %+v", ev)
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
	home := testsupport.TempDir(t)
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
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	w := &Workspace{sessions: map[string]*wsSession{}}
	// Gate with an explicit policy (so SetRules isn't a no-op) + an allow-all
	// confirmer, so a tool is allowed unless a deny RULE blocks it — isolating
	// the rule effect from builtin classification.
	gate := core.NewPolicyGate(&core.PermissionPolicy{Mode: core.ApprovalWorkspace}, allowConfirmer{})
	s := &wsSession{id: "x", hub: newWSHub(), ws: w, gate: gate, args: build.Args{}}
	w.sessions["x"] = s

	// A tool the confirmer would allow.
	if allowed, _, _ := gate.Check("mytool", nil, "", ""); !allowed {
		t.Fatal("precondition: mytool should be allowed by the confirmer")
	}

	if err := s.permissionsAction("add_rule", map[string]string{"tool": "mytool", "decision": "deny", "reason": "nope"}); err != nil {
		t.Fatalf("add_rule: %v", err)
	}
	// Live: the deny rule now blocks mytool (deny beats the confirmer).
	if allowed, _, _ := gate.Check("mytool", nil, "", ""); allowed {
		t.Error("deny rule should block mytool live")
	}
	if cfg, _ := config.LoadConfig(); len(cfg.Permissions) != 1 || cfg.Permissions[0].Tool != "mytool" {
		t.Errorf("rule not persisted: %+v", cfg.Permissions)
	}
	if err := s.permissionsAction("add_rule", map[string]string{"tool": "x", "decision": "bogus"}); err == nil {
		t.Error("bad decision should error")
	}
	// Remove restores it live.
	if err := s.permissionsAction("remove_rule", map[string]string{"tool": "mytool", "decision": "deny"}); err != nil {
		t.Fatalf("remove_rule: %v", err)
	}
	if allowed, _, _ := gate.Check("mytool", nil, "", ""); !allowed {
		t.Error("after removing the deny rule, mytool should be allowed again")
	}
	if cfg, _ := config.LoadConfig(); len(cfg.Permissions) != 0 {
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
	cwd := testsupport.TempDir(t)
	rule := config.PermissionRuleConfig{Tool: "bash", Decision: "deny", Reason: "no"}
	if err := setProjectPermissionRule(cwd, rule, true); err != nil {
		t.Fatalf("add project rule: %v", err)
	}
	if pc, err := config.LoadProjectConfig(cwd); err != nil || pc == nil || len(pc.Permissions) != 1 || pc.Permissions[0].Tool != "bash" {
		t.Fatalf("project rule not written: %+v (err %v)", pc, err)
	}
	_ = setProjectPermissionRule(cwd, rule, true) // idempotent
	if pc, _ := config.LoadProjectConfig(cwd); len(pc.Permissions) != 1 {
		t.Errorf("duplicate project rule: %+v", pc.Permissions)
	}
	if err := setProjectPermissionRule(cwd, config.PermissionRuleConfig{Tool: "bash", Decision: "deny"}, false); err != nil {
		t.Fatalf("remove project rule: %v", err)
	}
	if pc, _ := config.LoadProjectConfig(cwd); pc != nil && len(pc.Permissions) != 0 {
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
	dir := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", dir)
	sess, err := core.NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close() // release the file handle so TempDir cleanup works on Windows
	ag := core.NewAgent(nil, "fake", "", core.Registry{})
	ag.SetMessages([]provider.Message{{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hello"}}}})
	s := &wsSession{id: "x", hub: newWSHub(), agent: ag, sess: sess}
	sub := s.hub.add(nil, false)

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
	dir := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", dir)
	sess, err := core.NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close() // release the file handle so TempDir cleanup works on Windows
	// A real agent so setTrusted's rebuildTools is safe even if Resolve succeeds;
	// nil extMgr takes the rebuildTools branch.
	s := &wsSession{id: "x", hub: newWSHub(), sess: sess, agent: core.NewAgent(nil, "fake", "", core.Registry{})}
	w := &Workspace{cwd: dir, args: build.Args{CWD: dir}, sessions: map[string]*wsSession{"x": s}}
	sub := s.hub.add(nil, false)

	if err := w.Trust(context.Background(), false); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	if !s.trusted.Load() {
		t.Fatal("session should be trusted after Trust")
	}
	if !s.info().Trusted {
		t.Fatal("SessionInfo.Trusted should reflect the grant")
	}
	if !build.ResolveTrustState(w.args).IsTrusted() {
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
	if build.ResolveTrustState(w.args).IsTrusted() {
		t.Fatal("Untrust should drop the workspace from the trust store")
	}
}

// SessionInfo carries the session's subscription flag — the daemon resolved
// the credential, so it (not the client) knows whether cost is metered. The
// TUI's status bar tags cost "(sub)" off this when attached.
func TestSessionInfoSubscription(t *testing.T) {
	dir := testsupport.TempDir(t)
	sess, err := core.NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close() // release the file handle so TempDir cleanup works on Windows
	s := &wsSession{id: "x", hub: newWSHub(), sess: sess, subscription: true}
	if !s.info().Subscription {
		t.Fatal("SessionInfo.Subscription should reflect the session's OAuth credential")
	}
}

// ListFiles serves the @-file picker over the wire: entries relative to the
// workspace cwd in wire (slash) form, gitignore honored, and a
// client-supplied dir that escapes the cwd refused — the param arrives off
// the network.
func TestWorkspaceListFiles(t *testing.T) {
	dir := testsupport.TempDir(t)
	for rel, content := range map[string]string{
		".gitignore":  "dist/\n",
		"src/main.go": "x",
		"dist/out.js": "x",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w := &Workspace{cwd: dir}

	res, err := w.ListFiles(context.Background(), ctrlproto.FilesListParams{Recursive: true, RespectGitignore: true})
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	var paths []string
	for _, f := range res.Files {
		paths = append(paths, f.Path)
	}
	joined := strings.Join(paths, ",")
	if !strings.Contains(joined, "src/main.go") {
		t.Fatalf("listing missing src/main.go: %v", paths)
	}
	if strings.Contains(joined, "dist") {
		t.Fatalf("listing surfaced gitignored dist/: %v", paths)
	}

	if _, err := w.ListFiles(context.Background(), ctrlproto.FilesListParams{Dir: "../outside"}); err == nil {
		t.Fatal("ListFiles accepted a dir escaping the workspace cwd")
	}
}

// TestCreateSessionPersonaValidation covers the CreateOpts.Persona honesty fix:
// an unknown persona is rejected up front with CodeBadRequest rather than being
// silently dropped (or surfacing as a CodeInternal from the deferred Resolve).
func TestCreateSessionPersonaValidation(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
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
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	defer i18n.Configure("en", "")

	s := newTestSession()
	s.ws = &Workspace{} // BroadcastAll over an empty session set is a no-op

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
	if cfg, _ := config.LoadConfig(); cfg.Language != "fi" {
		t.Errorf("config.language = %q, want fi (should persist)", cfg.Language)
	}
	if err := s.settingsAction("set", map[string]string{"key": "language", "value": "zz-not-a-lang"}); err == nil {
		t.Error("unknown language should error")
	}
}

// TestSettingsLazyTools: the settings surface exposes the lazy-tool-loading
// config toggle (retro H2·b), and setting it persists to config (applies to new
// sessions — it is resolved at session build, so it does not reshape a running
// session live).
func TestSettingsLazyTools(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	s := newTestSession()
	s.ws = &Workspace{} // BroadcastAll over an empty session set is a no-op

	find := func() *ctrlproto.SettingItem {
		sv := s.settingsView()
		for i := range sv.Items {
			if sv.Items[i].Key == "lazy_tools" {
				return &sv.Items[i]
			}
		}
		return nil
	}

	it := find()
	if it == nil || it.Type != "bool" {
		t.Fatalf("no lazy_tools bool row in the settings view: %+v", s.settingsView().Items)
	}
	if it.Value != "false" {
		t.Errorf("lazy_tools default = %q, want false", it.Value)
	}

	if err := s.settingsAction("set", map[string]string{"key": "lazy_tools", "value": "true"}); err != nil {
		t.Fatalf("enable lazy_tools: %v", err)
	}
	if cfg, _ := config.LoadConfig(); !cfg.LazyTools {
		t.Error("enabling lazy_tools must persist to config")
	}
	if it := find(); it == nil || it.Value != "true" {
		t.Errorf("the view should reflect lazy_tools=true, got %+v", it)
	}
}

// TestSettingsWebStage: the Stage surface toggle persists web_stage to config
// (the config twin of --web-stage that `terva web` reads at startup) and the
// view reflects it — default off.
func TestSettingsWebStage(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	s := newTestSession()
	s.ws = &Workspace{}

	find := func() *ctrlproto.SettingItem {
		sv := s.settingsView()
		for i := range sv.Items {
			if sv.Items[i].Key == "web_stage" {
				return &sv.Items[i]
			}
		}
		return nil
	}

	it := find()
	if it == nil || it.Type != "bool" {
		t.Fatalf("no web_stage bool row in the settings view")
	}
	if it.Value != "false" {
		t.Errorf("web_stage default = %q, want false", it.Value)
	}
	if err := s.settingsAction("set", map[string]string{"key": "web_stage", "value": "true"}); err != nil {
		t.Fatalf("enable web_stage: %v", err)
	}
	if cfg, _ := config.LoadConfig(); !cfg.WebStage {
		t.Error("enabling web_stage must persist to config")
	}
	if it := find(); it == nil || it.Value != "true" {
		t.Errorf("the view should reflect web_stage=true, got %+v", it)
	}
}

// TestSettingsSweep covers the config-surface sweep: the tricky cases —
// default-on polarity (respect_gitignore), the inverted disable-flag
// (core_pack_offer), temperature preset parsing + range, theme auto↔"", and
// enum validation.
func TestSettingsSweep(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	s := newTestSession()
	s.ws = &Workspace{} // BroadcastAll over an empty session set is a no-op

	val := func(key string) string {
		for _, it := range s.settingsView().Items {
			if it.Key == key {
				return it.Value
			}
		}
		t.Fatalf("no %q setting in the view", key)
		return ""
	}
	set := func(key, v string) error {
		return s.settingsAction("set", map[string]string{"key": key, "value": v})
	}

	// respect_gitignore defaults ON (nil polarity).
	if v := val("respect_gitignore"); v != "true" {
		t.Errorf("respect_gitignore default = %q, want true (nil = on)", v)
	}
	// core_pack_offer is the inverted view of DisableCorePackOffer.
	if v := val("core_pack_offer"); v != "true" {
		t.Errorf("core_pack_offer default = %q, want true (offer allowed)", v)
	}
	if err := set("core_pack_offer", "false"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := config.LoadConfig(); !cfg.DisableCorePackOffer {
		t.Error("turning the offer off must set DisableCorePackOffer=true")
	}

	// auto_compact: valid round-trip; invalid rejected.
	if err := set("auto_compact", "off"); err != nil {
		t.Fatal(err)
	}
	if v := val("auto_compact"); v != "off" {
		t.Errorf("auto_compact = %q, want off", v)
	}
	if err := set("auto_compact", "bogus"); err == nil {
		t.Error("an invalid auto_compact value must error")
	}

	// temperature: a preset persists a non-nil *float32 that round-trips; an
	// out-of-range value is rejected; "" clears to the model default.
	if err := set("temperature", "0.7"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := config.LoadConfig(); cfg.Temperature == nil {
		t.Error("temperature 0.7 must persist a non-nil value")
	}
	if v := val("temperature"); v != "0.7" {
		t.Errorf("temperature view = %q, want 0.7", v)
	}
	if err := set("temperature", "9"); err == nil {
		t.Error("an out-of-range temperature must error")
	}
	if err := set("temperature", ""); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := config.LoadConfig(); cfg.Temperature != nil {
		t.Error("an empty temperature must clear to nil (model default)")
	}

	// theme: "auto" is stored as the canonical empty string; a real theme
	// round-trips and an empty stored theme displays as "auto".
	if err := set("theme", "dark"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := config.LoadConfig(); cfg.Theme != "dark" {
		t.Errorf("theme = %q, want dark", cfg.Theme)
	}
	if err := set("theme", "auto"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := config.LoadConfig(); cfg.Theme != "" {
		t.Errorf("auto theme must store as empty, got %q", cfg.Theme)
	}
	if v := val("theme"); v != "auto" {
		t.Errorf("an empty stored theme should display as auto, got %q", v)
	}

	// a plain pointer-bool: swarm_worktrees defaults off, persists on.
	if v := val("swarm_worktrees"); v != "false" {
		t.Errorf("swarm_worktrees default = %q, want false", v)
	}
	if err := set("swarm_worktrees", "true"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := config.LoadConfig(); cfg.SwarmWorktrees == nil || !*cfg.SwarmWorktrees {
		t.Error("swarm_worktrees must persist true")
	}
}

// TestExtPanelSurface covers the extension-panel bridge: an opened panel joins
// the registry, broadcasts, is fetchable, and leaves on close.
func TestExtPanelSurface(t *testing.T) {
	s := &wsSession{id: "x", hub: newWSHub(), agent: core.NewAgent(nil, "fake", "", core.Registry{}), extPanels: map[string]*webPanel{}}
	sub := s.hub.add(nil, false)

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
	tmp := testsupport.TempDir(t)
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
	// Isolate the home: the approval/auto-swarm verbs now trigger a live
	// rebuild whose Resolve must not read (or repair) the real user config.
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	pol, _ := build.BuildPermissionPolicy(build.Args{Mode: build.ModeWeb})
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
