package modes

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

func newCtrlprotoTestInteractive() *Interactive {
	th := tui.Dark
	return &Interactive{
		cfg:            InteractiveConfig{Theme: th},
		dirty:          make(chan struct{}, 8),
		turns:          newTurnEngine(),
		toolCalls:      map[string]*tui.ToolCallView{},
		view:           &tui.View{Theme: th},
		spin:           widgets.NewSpinner(th),
		confirmDialog:  dialogs.NewConfirmDialog(),
		questionDialog: dialogs.NewQuestionDialog(),
		rescueDialog:   dialogs.NewRescueDialog(),
		carrierPerm:    map[string]*dialogs.ConfirmRequest{},
		carrierAsk:     map[string]*dialogs.QuestionRequest{},
	}
}

func conv(w core.WireEvent) ctrlproto.Event { return ctrlproto.ConversationEvent(w) }

// fakeCarrier implements the Carrier seam for dispatch/approval tests. The
// embedded interface covers the verbs a test doesn't exercise (calling one
// panics, which IS the assertion that the TUI doesn't stray off the hot path).
type fakeCarrier struct {
	ctrlproto.WorkspaceService
	mu        sync.Mutex
	infos     map[string]ctrlproto.SessionInfo
	prompts   chan string
	queued    chan string
	approves  chan approvedCall
	answers   chan answeredAsk
	cancels   chan struct{}
	compacts  chan struct{}
	clears    chan struct{}
	subs      chan string    // session id of each SubscribeReliable call
	switches  chan [2]string // provider+model of each SwitchModel call
	surfActs  chan surfAct   // every SurfaceAction call
	promptErr error
	// tasks is the tasks-surface fixture (mutable so cache-invalidation tests
	// can move the daemon state under the TUI). surfActErr, when non-nil, is
	// returned by every SurfaceAction after recording it; surfErr, when
	// non-nil, makes every Surface read fail (a daemon that went away).
	chat       ctrlproto.ChatView // the chat pane the TUI mirrors
	tasks      []ctrlproto.TaskInfo
	surfActErr error
	surfErr    error
	// extView, when non-nil, replaces the hardcoded extensions surface — for
	// tests that need config forms on the wire (ext_config_carrier_test.go).
	extView *ctrlproto.ExtensionsView
	// compactGate, when non-nil, parks Compact until closed — so a test can
	// observe the busy slot held across the round-trip.
	compactGate chan struct{}
	// stream, when non-nil, is what SubscribeReliable returns — the test
	// plays the daemon by feeding events into it. When nil, each subscribe
	// gets its own channel, closed when its ctx cancels (like the real hub).
	// subErr, when non-nil, fails every subscribe (the pump fail-stop tests).
	stream chan ctrlproto.Event
	// wsStream, when non-nil, is what a subscribe to the WORKSPACE address
	// (ctrlproto.AddrWorkspace) returns. Deliberately separate from stream: the
	// workspace is a different address with a different hub behind it, and a fake
	// that handed both pumps the same channel would let the workspace pump race
	// the session pump and eat its events.
	wsStream chan ctrlproto.Event
	subErr   error
	// panels / metas back the extension-panel surface fixtures (guarded by mu):
	// Surface serves panels by id, Surfaces returns metas.
	panels map[string]ctrlproto.Surface
	metas  []ctrlproto.SurfaceMeta
	// onSurface, when set, runs inside Surface before it returns — a seam for
	// modeling a session switch (or any state change) landing while a fetch is
	// still in flight. Called with no lock held.
	onSurface func(id string)
	// setQueues records every SetQueue call, in order (guarded by mu).
	setQueues [][]string
	// usage is the usage.snapshot fixture; usageCalls records each call's
	// refresh flag, usageErr fails every call (all guarded by mu).
	usage      ctrlproto.UsageInfo
	usageCalls []bool
	usageErr   error
}

type approvedCall struct {
	sess   string
	callID string
	d      core.ConfirmDecision
}

type answeredAsk struct {
	askID string
	a     core.UserAnswer
}

func newFakeCarrier() *fakeCarrier {
	return &fakeCarrier{
		prompts:  make(chan string, 4),
		queued:   make(chan string, 4),
		approves: make(chan approvedCall, 4),
		answers:  make(chan answeredAsk, 4),
		cancels:  make(chan struct{}, 4),
		compacts: make(chan struct{}, 4),
		clears:   make(chan struct{}, 4),
		subs:     make(chan string, 4),
		switches: make(chan [2]string, 4),
		surfActs: make(chan surfAct, 8),
	}
}

func (f *fakeCarrier) Prompt(ctx context.Context, sess, text string, images []ctrlproto.Image) error {
	if f.promptErr != nil {
		return f.promptErr
	}
	f.prompts <- text
	return nil
}

func (f *fakeCarrier) Queue(ctx context.Context, sess, text string) error {
	f.queued <- text
	return nil
}

// SetQueue records the list the TUI committed. The real daemon answers with a
// queue_updated broadcast; tests that care feed one back through the stream.
func (f *fakeCarrier) SetQueue(ctx context.Context, sess string, texts []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setQueues = append(f.setQueues, append([]string(nil), texts...))
	return nil
}

// UsageSnapshot records each call and serves the fixture. The TUI refreshes
// the usage mirror on every `done` event and every snapshot, so a fake that
// left this to the embedded nil WorkspaceService would panic, not fail to
// compile.
func (f *fakeCarrier) UsageSnapshot(ctx context.Context, sess string, refresh bool) (ctrlproto.UsageInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.usageCalls = append(f.usageCalls, refresh)
	if f.usageErr != nil {
		return ctrlproto.UsageInfo{}, f.usageErr
	}
	return f.usage, nil
}

func (f *fakeCarrier) Cancel(ctx context.Context, sess string) error {
	f.cancels <- struct{}{}
	return nil
}

func (f *fakeCarrier) Approve(ctx context.Context, sess, callID string, d core.ConfirmDecision) error {
	f.approves <- approvedCall{sess, callID, d}
	return nil
}

func (f *fakeCarrier) Answer(ctx context.Context, sess, askID string, a core.UserAnswer) error {
	f.answers <- answeredAsk{askID, a}
	return nil
}

func (f *fakeCarrier) Compact(ctx context.Context, sess string) error {
	if f.compactGate != nil {
		<-f.compactGate
	}
	f.compacts <- struct{}{}
	return nil
}

func (f *fakeCarrier) Clear(ctx context.Context, sess string) error {
	f.clears <- struct{}{}
	return nil
}

func (f *fakeCarrier) SubscribeReliable(ctx context.Context, sess string) (<-chan ctrlproto.Event, error) {
	if f.subErr != nil {
		return nil, f.subErr
	}
	// The workspace is not a session: it is never recorded in subs (which tests
	// read to assert which SESSION the pump bound to), and it has its own stream.
	if sess == ctrlproto.AddrWorkspace {
		if f.wsStream != nil {
			return f.wsStream, nil
		}
		return closedOnCancel(ctx), nil
	}
	select {
	case f.subs <- sess:
	default:
	}
	if f.stream != nil {
		return f.stream, nil
	}
	return closedOnCancel(ctx), nil
}

// closedOnCancel is an idle subscription: no events, closed when its ctx ends —
// what the real hub does for a subscriber nothing is broadcasting to.
func closedOnCancel(ctx context.Context) chan ctrlproto.Event {
	ch := make(chan ctrlproto.Event, 64)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch
}

func (f *fakeCarrier) SwitchModel(ctx context.Context, sess, providerName, modelID string) error {
	f.switches <- [2]string{providerName, modelID}
	return nil
}

func (f *fakeCarrier) Surface(ctx context.Context, sess, id string) (ctrlproto.Surface, error) {
	if f.surfErr != nil {
		return ctrlproto.Surface{}, f.surfErr
	}
	if f.onSurface != nil {
		f.onSurface(id)
	}
	switch id {
	case "chat":
		f.mu.Lock()
		cv := f.chat
		f.mu.Unlock()
		return ctrlproto.Surface{ID: id, Kind: "chat", Chat: &cv}, nil
	case "permissions":
		return ctrlproto.Surface{ID: id, Kind: "permissions", Permissions: &ctrlproto.PermissionsView{
			Mode: "safe",
			Rules: []ctrlproto.PermissionRule{
				{Tool: "bash", Args: "rm .*", Decision: "deny", Source: "user", Reason: "no deletes"},
			},
			AllowAll: true,
			Grants:   []string{"write"},
		}}, nil
	case "extensions":
		if f.extView != nil {
			return ctrlproto.Surface{ID: id, Kind: "extensions", Extensions: f.extView}, nil
		}
		return ctrlproto.Surface{ID: id, Kind: "extensions", Extensions: &ctrlproto.ExtensionsView{
			Extensions: []ctrlproto.ExtensionInfo{
				{Name: "memory", Scope: "global", Status: "running", Enabled: true,
					GlobalEnabled: true, Tools: 2, Commands: 1},
				{Name: "linter", Scope: "project", Status: "gated", Enabled: false,
					GlobalEnabled: true, ProjectDisabled: false},
				{Name: "old", Scope: "global", Status: "disabled", Enabled: false,
					UserConfigDisabled: true, Note: "crashed: exit 1"},
			},
		}}, nil
	case "mcp":
		return ctrlproto.Surface{ID: id, Kind: "mcp", MCP: &ctrlproto.MCPView{
			Servers: []ctrlproto.MCPServerInfo{
				{Name: "files", Scope: "global", Status: "running", Enabled: true, Connected: true, Tools: 4},
				{Name: "broken", Scope: "project", Status: "failed", Enabled: true, Note: "spawn: no such file"},
			},
		}}, nil
	case "settings":
		return ctrlproto.Surface{ID: id, Kind: "settings", Settings: &ctrlproto.SettingsView{
			Items: []ctrlproto.SettingItem{
				{Key: "approval", Label: "Approval mode", Type: "enum", Value: "workspace",
					Description: "How tool calls are gated for this session.",
					Note:        "per-session — not saved",
					Options: []ctrlproto.SettingOption{
						{Value: "plan", Label: "plan"},
						{Value: "ask", Label: "ask"},
						{Value: "auto-edit", Label: "auto-edit"},
						{Value: "workspace", Label: "workspace"},
						{Value: "yolo", Label: "yolo"},
					}},
				{Key: "lazy_tools", Label: "Lazy tool loading", Type: "bool", Value: "true"},
			},
		}}, nil
	case "lore":
		return ctrlproto.Surface{ID: id, Kind: "lore", Lore: &ctrlproto.LoreView{
			Entries: []ctrlproto.LoreEntry{{Name: "world", Keys: []string{"kantele"}, Source: "user"}},
		}}, nil
	case "tasks":
		f.mu.Lock()
		ts := append([]ctrlproto.TaskInfo(nil), f.tasks...)
		f.mu.Unlock()
		return ctrlproto.Surface{ID: id, Kind: "tasks", Tasks: &ctrlproto.TaskList{Tasks: ts}}, nil
	}
	f.mu.Lock()
	p, ok := f.panels[id]
	f.mu.Unlock()
	if ok {
		return p, nil
	}
	return ctrlproto.Surface{}, ctrlproto.ErrNotFound
}

func (f *fakeCarrier) Surfaces(ctx context.Context, sess string) ([]ctrlproto.SurfaceMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ctrlproto.SurfaceMeta(nil), f.metas...), nil
}

func (f *fakeCarrier) SurfaceAction(ctx context.Context, sess, id, action string, args map[string]string) error {
	f.surfActs <- surfAct{id: id, action: action, args: args}
	return f.surfActErr
}

type surfAct struct {
	id, action string
	args       map[string]string
}

func (f *fakeCarrier) Context(ctx context.Context, sess string) (ctrlproto.ContextBreakdown, error) {
	return ctrlproto.ContextBreakdown{
		Model: "fake-model", Window: 200000,
		SystemBytes: 1000, ToolBytes: 500, ToolCount: 3, ExtBytes: 200,
		TranscriptBytes: 900, TotalBytes: 2600,
		Messages: []ctrlproto.ContextMessage{
			{Index: 0, Kind: "user", Bytes: 100},
			{Index: 1, Kind: "tool_result", Bytes: 800},
		},
	}, nil
}

func (f *fakeCarrier) ResumeSession(ctx context.Context, sess string) (ctrlproto.SessionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if info, ok := f.infos[sess]; ok {
		return info, nil
	}
	return ctrlproto.SessionInfo{ID: sess}, nil
}

func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for %s", what)
		panic("unreachable")
	}
}

// TestCarrierTurnLifecycle: the local slot re-arms on a daemon-initiated
// turn_start, releases on the definitive done (idempotently — the
// cancel-during-tools path produces two), and error paints the status banner.
func TestCarrierTurnLifecycle(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = newFakeCarrier()

	i.handleCarrierEvent(conv(core.WireEvent{Type: "turn_start", Step: 1}))
	if !i.turns.Busy() {
		t.Fatal("turn_start should claim the local slot")
	}
	i.handleCarrierEvent(conv(core.WireEvent{Type: "error", Error: "provider unavailable"}))
	if i.statusErr != "provider unavailable" {
		t.Fatalf("statusErr = %q", i.statusErr)
	}
	i.handleCarrierEvent(conv(core.WireEvent{Type: "done"}))
	if i.turns.Busy() {
		t.Fatal("done should release the slot")
	}
	i.handleCarrierEvent(conv(core.WireEvent{Type: "done"})) // duplicate: no-op
	if i.turns.Busy() {
		t.Fatal("duplicate done must stay released")
	}
}

// TestCarrierCompactLifecycle: daemon-side policy compaction events drive the
// auto-compacting flag, the condensing note, and the post-compact status.
func TestCarrierCompactLifecycle(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = newFakeCarrier()

	i.handleCarrierEvent(conv(core.WireEvent{Type: "turn_start", Step: 1}))
	i.handleCarrierEvent(conv(core.WireEvent{Type: "compact_start", Text: "context near limit"}))
	if !i.turns.AutoCompacting() {
		t.Fatal("compact_start should mark auto-compacting")
	}
	if len(i.extNotes) == 0 {
		t.Fatal("compact_start should add the condensing note")
	}
	i.handleCarrierEvent(conv(core.WireEvent{Type: "compact_end"}))
	if i.turns.AutoCompacting() {
		t.Fatal("compact_end should clear auto-compacting")
	}
	if i.statusOK == "" || i.pendingPostCompactNote != "" {
		t.Fatalf("post-compact status not settled: ok=%q pending=%q", i.statusOK, i.pendingPostCompactNote)
	}

	// A failed compaction paints the failure instead.
	i.handleCarrierEvent(conv(core.WireEvent{Type: "compact_start", Text: "request too large; retrying"}))
	i.handleCarrierEvent(conv(core.WireEvent{Type: "compact_end", Error: "summarize: boom"}))
	if i.statusErr == "" {
		t.Fatal("failed compact_end should set statusErr")
	}
}

// TestStartTurnCarrierDispatch: an idle dispatch claims the slot and Prompts
// through the service; a second producer while busy queues through the service
// (queue_updated convergence), never onto the agent directly.
func TestStartTurnCarrierDispatch(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	i.cfg.Carrier = fc

	i.startTurnCarrier(context.Background(), "first", nil)
	if !i.turns.Busy() {
		t.Fatal("dispatch should claim the local slot")
	}
	if got := recv(t, fc.prompts, "prompt"); got != "first" {
		t.Fatalf("Prompt text = %q", got)
	}

	i.startTurnCarrier(context.Background(), "second", nil)
	if got := recv(t, fc.queued, "queue"); got != "second" {
		t.Fatalf("Queue text = %q", got)
	}
}

// TestStartTurnCarrierBusyRace: the daemon refusing the dispatch with
// CodeBusy (another client won the race) releases the local slot and re-routes
// the prompt through Queue so the text isn't lost.
func TestStartTurnCarrierBusyRace(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	fc.promptErr = ctrlproto.ErrBusy
	i.cfg.Carrier = fc

	i.startTurnCarrier(context.Background(), "raced", nil)
	if got := recv(t, fc.queued, "queue fallback"); got != "raced" {
		t.Fatalf("Queue fallback text = %q", got)
	}
	if i.turns.Busy() {
		t.Fatal("refused dispatch should release the local slot")
	}
}

// TestCarrierCancelRoutesToService: esc/ctrl+c cancel plumbing (cancelActive)
// must reach the service's Cancel verb in carrier mode.
func TestCarrierCancelRoutesToService(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	i.cfg.Carrier = fc

	i.startTurnCarrier(context.Background(), "x", nil)
	recv(t, fc.prompts, "prompt")
	if !i.turns.cancelActive() {
		t.Fatal("cancelActive should find the carrier cancel func")
	}
	recv(t, fc.cancels, "cancel")
}

// TestCarrierPermissionInversion: a wire permission request drives the
// existing confirm dialog; the user's key answers it back through Approve;
// a resolved event for an unanswered request dismisses the dialog silently.
func TestCarrierPermissionInversion(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	i.cfg.Carrier = fc

	i.handleCarrierEvent(ctrlproto.PermissionEvent(ctrlproto.PermissionRequest{
		CallID: "c1", Tool: "write", Preview: "main.go",
	}))
	if !i.confirmDialog.Active() {
		t.Fatal("permission request should open the confirm dialog")
	}
	i.confirmDialog.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: '1'}) // yes
	got := recv(t, fc.approves, "approve")
	if got.callID != "c1" || !got.d.Allow {
		t.Fatalf("approve = %+v", got)
	}
	i.mu.Lock()
	left := len(i.carrierPerm)
	i.mu.Unlock()
	if left != 0 {
		t.Fatalf("carrierPerm should be empty, has %d", left)
	}

	// Another client resolves the next one: dismiss without answering.
	i.handleCarrierEvent(ctrlproto.PermissionEvent(ctrlproto.PermissionRequest{CallID: "c2", Tool: "bash"}))
	i.handleCarrierEvent(ctrlproto.PermissionResolvedEvent("c2"))
	if i.confirmDialog.Active() {
		t.Fatal("resolved event should dismiss the pending dialog")
	}
	select {
	case a := <-fc.approves:
		t.Fatalf("dismissal must not answer, got %+v", a)
	default:
	}
}

// TestSwapModelCarrier: /model (and rescue) selections route through
// SwitchModel with the provider-qualified id, and the post-swap identity is
// read back from the service.
func TestSwapModelCarrier(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	i.cfg.Carrier = fc
	i.cfg.CarrierSession = "s1"
	fc.infos = map[string]ctrlproto.SessionInfo{
		"s1": {ID: "s1", Provider: "openai-codex", Model: "gpt-5.5"},
	}

	i.applyModelSelection("openai-codex", "gpt-5.5")
	got := recv(t, fc.switches, "switch")
	if got[0] != "openai-codex" || got[1] != "gpt-5.5" {
		t.Fatalf("SwitchModel args = %v", got)
	}
	i.mu.Lock()
	prov, model, ok := i.cfg.Provider, i.cfg.Model, i.statusOK
	i.mu.Unlock()
	if prov != "openai-codex" || model != "gpt-5.5" {
		t.Fatalf("post-swap identity = %s/%s", prov, model)
	}
	if ok == "" {
		t.Fatal("swap should confirm on the status line")
	}
}

// TestCarrierRescueOnRecoverableError: a recoverable wire error opens the
// rescue picker with the failed turn's prompt (banner suppressed); a
// non-recoverable one lands on the status banner instead.
func TestCarrierRescueOnRecoverableError(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	i.cfg.Carrier = fc

	i.startTurnCarrier(context.Background(), "do the thing", nil)
	recv(t, fc.prompts, "dispatch")
	i.handleCarrierEvent(conv(core.WireEvent{Type: "error", Error: "anthropic: http 429: rate limited"}))
	if !i.rescueDialog.Active() {
		t.Fatal("recoverable error should open the rescue picker")
	}
	if i.statusErr != "" {
		t.Fatalf("banner should be suppressed while rescuing, got %q", i.statusErr)
	}
	i.mu.Lock()
	pending := i.pendingRescuePrompt
	i.mu.Unlock()
	if pending != "do the thing" {
		t.Fatalf("pending rescue prompt = %q", pending)
	}

	// Non-recoverable: banner, no dialog.
	j := newCtrlprotoTestInteractive()
	j.cfg.Carrier = fc
	j.handleCarrierEvent(conv(core.WireEvent{Type: "error", Error: "invalid request: bad schema"}))
	if j.rescueDialog.Active() {
		t.Fatal("non-recoverable error must not open the rescue picker")
	}
	if j.statusErr == "" {
		t.Fatal("non-recoverable error should land on the banner")
	}
}

// TestCarrierRescueTracksForeignTurn: a queue-restarted (daemon-initiated)
// turn's user_message updates the rescue prompt so the retry re-fires the
// right text — and drops locally-stashed images that belong to the prior turn.
func TestCarrierRescueTracksForeignTurn(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	i.cfg.Carrier = fc

	i.startTurnCarrier(context.Background(), "first", []provider.ImageBlock{{MimeType: "image/png", Data: []byte("x")}})
	recv(t, fc.prompts, "dispatch")
	i.handleCarrierEvent(conv(core.WireEvent{
		Type:    "user_message",
		Message: &core.WireMessage{Role: "user", Content: []core.WireBlock{{Type: "text", Text: "queued follow-up"}}},
	}))
	i.mu.Lock()
	prompt, imgs := i.carrierLastPrompt, i.carrierLastImages
	i.mu.Unlock()
	if prompt != "queued follow-up" || imgs != nil {
		t.Fatalf("foreign turn tracking = %q imgs=%d", prompt, len(imgs))
	}
}

// TestCarrierContextOverview: /context's Overview renders from the service's
// ContextBreakdown. There is no longer a second, in-process assembly to keep in
// parity with — the daemon computes the breakdown and the TUI only paints it.
func TestCarrierContextOverview(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = newFakeCarrier()

	lines := i.buildContextOverview(i.cfg.Theme)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		i18n.T("system prompt"), i18n.T("tool defs"), "tool_result",
		i18n.T("← largest"), i18n.T("TOTAL"),
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("overview missing %q:\n%s", want, joined)
		}
	}
}

// TestCarrierPermissionsInspector: the /permissions inspector renders from
// the wire PermissionsView, and revoke/reset resolve through the surface's
// action vocabulary (reset composes revoke_all + per-tool revokes).
func TestCarrierPermissionsInspector(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	i.cfg.Carrier = fc

	info, grants := i.buildPermissionsView()
	joined := strings.Join(info, "\n")
	for _, want := range []string{i18n.T("approval mode"), "safe", "bash", "no deletes"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("inspector missing %q:\n%s", want, joined)
		}
	}
	if len(grants) != 2 || !grants[0].AllowAll || grants[1].Tool != "write" {
		t.Fatalf("grants = %+v", grants)
	}

	i.carrierPermissionRevoke(dialogs.PermGrant{Tool: "write"})
	if act := recv(t, fc.surfActs, "revoke"); act.action != "revoke" || act.args["tool"] != "write" {
		t.Fatalf("revoke action = %+v", act)
	}
	i.carrierPermissionRevoke(dialogs.PermGrant{AllowAll: true})
	if act := recv(t, fc.surfActs, "revoke_all"); act.action != "revoke_all" {
		t.Fatalf("revoke_all action = %+v", act)
	}

	i.carrierPermissionsReset()
	first := recv(t, fc.surfActs, "reset revoke_all")
	second := recv(t, fc.surfActs, "reset revoke")
	if first.action != "revoke_all" || second.action != "revoke" || second.args["tool"] != "write" {
		t.Fatalf("reset composition = %+v then %+v", first, second)
	}
}

// TestCarrierExtensionsAndMCP: the /extensions and /mcp dialogs read the
// enriched wire views (toggle-scope detail + status-derived flags) and their
// toggles ride the surface action vocabulary with the right scope.
func TestCarrierExtensionsAndMCP(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	i.cfg.Carrier = fc

	exts := i.carrierListExtensions()
	if len(exts) != 3 {
		t.Fatalf("extensions = %d, want 3", len(exts))
	}
	if !exts[0].Running || !exts[0].GlobalEnabled || !exts[0].Effective {
		t.Fatalf("running ext flags = %+v", exts[0])
	}
	if !exts[1].ProjectGated || exts[1].Running {
		t.Fatalf("gated ext flags = %+v", exts[1])
	}
	if !exts[2].UserConfigDisabled || exts[2].LastLog != "crashed: exit 1" {
		t.Fatalf("disabled ext flags = %+v", exts[2])
	}

	i.applyCarrierExtensionToggle(dialogs.ExtensionsAction{Name: "linter", On: true, ToggleGlobal: true})
	if act := recv(t, fc.surfActs, "ext toggle"); act.id != "extensions" || act.action != "toggle" ||
		act.args["name"] != "linter" || act.args["enabled"] != "true" || act.args["scope"] != "global" {
		t.Fatalf("ext toggle action = %+v", act)
	}

	servers := i.carrierListMCP()
	if len(servers) != 2 {
		t.Fatalf("mcp servers = %d, want 2", len(servers))
	}
	if !servers[0].Connected || !servers[0].Effective {
		t.Fatalf("running server flags = %+v", servers[0])
	}
	if servers[1].Connected || servers[1].StartupError == "" {
		t.Fatalf("failed server flags = %+v", servers[1])
	}

	i.applyCarrierMCPToggle(dialogs.MCPAction{Name: "files", On: false, ToggleProject: true})
	if act := recv(t, fc.surfActs, "mcp toggle"); act.id != "mcp" || act.action != "toggle" ||
		act.args["name"] != "files" || act.args["enabled"] != "false" || act.args["scope"] != "project" {
		t.Fatalf("mcp toggle action = %+v", act)
	}
}

// TestCarrierSwarmDashboard: /swarm on the carrier path — the dashboard's
// snapshot reads come from the cached tasks surface (re-fetched only on the
// daemon's surface_updated signal), and every verb (spawn/stop/send/resume)
// rides SurfaceAction. The send path recovers the swarm.ErrNotReady sentinel
// from the wire's flattened error text so the "press R to resume" hint keeps
// working.
func TestCarrierSwarmDashboard(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	i.cfg.Carrier = fc
	i.cfg.CarrierTasks = true
	i.swarmDialog = dialogs.NewSwarmDialog()
	fc.tasks = []ctrlproto.TaskInfo{
		{ID: "a1", Task: "audit the parser", Status: "running", Activity: "editing",
			Model: "m1", Provider: "p1", Dir: "/repo", Started: "2026-07-04T10:00:00Z",
			Tail: "last line", Lines: []string{"first line", "last line"}},
		{ID: "a2", Task: "old run", Status: "done", Finished: "2026-07-04T09:00:00Z"},
	}

	ctx := context.Background()
	i.runSwarm(ctx, nil) // open the dashboard → first snapshot fetch
	if !i.swarmDialog.Active() {
		t.Fatal("dashboard should be open")
	}
	rows := i.swarmDialog.Rows()
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].ID != "a1" || rows[0].Status != swarm.StatusRunning || rows[0].Dir != "/repo" ||
		rows[0].Model != "m1" || len(rows[0].Lines) != 2 || rows[0].Started.IsZero() {
		t.Fatalf("mapped row = %+v", rows[0])
	}
	if n := i.swarmAgentCount(); n != 1 {
		t.Fatalf("swarmAgentCount = %d, want 1 (one running, one done)", n)
	}

	// The cache serves reads until the daemon signals a change.
	fc.mu.Lock()
	fc.tasks = fc.tasks[:1]
	fc.mu.Unlock()
	if got := len(i.carrierTaskSnapshot()); got != 2 {
		t.Fatalf("pre-signal snapshot = %d rows, want the cached 2", got)
	}
	i.handleCarrierEvent(ctrlproto.SurfaceUpdatedEvent("tasks"))
	// The signal only marks the cache stale — a render-path read kicks the
	// fill asynchronously (a frame must never block on the network). Run the
	// fill synchronously here so the assertion doesn't race it.
	i.fetchCarrierTasks()
	if got := len(i.carrierTaskSnapshot()); got != 1 {
		t.Fatalf("post-signal snapshot = %d rows, want 1", got)
	}

	// Every verb rides the tasks surface.
	i.runSwarm(ctx, []string{"kill", "a1"})
	if act := recv(t, fc.surfActs, "stop"); act.id != "tasks" || act.action != "stop" || act.args["id"] != "a1" {
		t.Fatalf("stop action = %+v", act)
	}
	i.runSwarm(ctx, []string{"new", "--model", "m2", "--persona", "tester", "run the suite"})
	if act := recv(t, fc.surfActs, "spawn"); act.action != "spawn" || act.args["task"] != "run the suite" ||
		act.args["model"] != "m2" || act.args["persona"] != "tester" {
		t.Fatalf("spawn action = %+v", act)
	}
	i.mu.Lock()
	spawned := i.statusOK
	i.mu.Unlock()
	// Surface actions return no payload, so the status can't name the new
	// agent's id — but it must still confirm what was requested.
	if !strings.Contains(spawned, "persona tester") {
		t.Fatalf("spawn status = %q", spawned)
	}
	i.runSwarm(ctx, []string{"resume", "a2"})
	if act := recv(t, fc.surfActs, "resume"); act.action != "resume" || act.args["id"] != "a2" {
		t.Fatalf("resume action = %+v", act)
	}

	// send: the sentinel survives the wire's text flattening.
	fc.surfActErr = ctrlproto.Errorf(ctrlproto.CodeInternal, "%v", swarm.ErrNotReady)
	i.runSwarm(ctx, []string{"send", "a1", "keep going"})
	if act := recv(t, fc.surfActs, "send"); act.action != "send" || act.args["text"] != "keep going" {
		t.Fatalf("send action = %+v", act)
	}
	i.mu.Lock()
	sendErr := i.statusErr
	i.mu.Unlock()
	if !strings.Contains(sendErr, "press R to resume") {
		t.Fatalf("send err = %q, want the not-ready hint", sendErr)
	}
}

// TestCarrierSwarmDisabled: a carrier session without the tasks gate (and no
// local swarm) reports the feature off instead of opening an empty dashboard.
func TestCarrierSwarmDisabled(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = newFakeCarrier()
	i.swarmDialog = dialogs.NewSwarmDialog()
	i.runSwarm(context.Background(), nil)
	if i.swarmDialog.Active() {
		t.Fatal("dashboard must not open when swarm is unavailable")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.statusErr == "" {
		t.Fatal("disabled path should set statusErr")
	}
}

// TestCarrierTranscriptAssembly: the pump-owned transcript reproduces the
// daemon's message assembly from the wire — snapshots replace it wholesale,
// message events append, and consecutive tool_result events fold into ONE
// RoleTool message per step (executeTools' batching), sealed by the next
// message event.
func TestCarrierTranscriptAssembly(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = newFakeCarrier()

	i.handleCarrierEvent(ctrlproto.SnapshotEvent(ctrlproto.Snapshot{
		Messages: []core.WireMessage{
			{Role: "user", Content: []core.WireBlock{{Type: "text", Text: "earlier"}}},
			{Role: "assistant", Content: []core.WireBlock{{Type: "text", Text: "history"}}},
		},
	}))
	i.handleCarrierEvent(conv(core.WireEvent{Type: "user_message",
		Message: &core.WireMessage{Role: "user", Content: []core.WireBlock{{Type: "text", Text: "do two things"}}}}))
	i.handleCarrierEvent(conv(core.WireEvent{Type: "assistant_message",
		Message: &core.WireMessage{Role: "assistant", Content: []core.WireBlock{
			{Type: "tool_call", ID: "t1", Name: "read"},
			{Type: "tool_call", ID: "t2", Name: "write"},
		}}}))
	i.handleCarrierEvent(conv(core.WireEvent{Type: "tool_result", ID: "t1",
		Result: []core.WireBlock{{Type: "text", Text: "r1"}}}))
	i.handleCarrierEvent(conv(core.WireEvent{Type: "tool_result", ID: "t2", IsError: true,
		Result: []core.WireBlock{{Type: "text", Text: "boom"}}}))
	i.handleCarrierEvent(conv(core.WireEvent{Type: "assistant_message",
		Message: &core.WireMessage{Role: "assistant", Content: []core.WireBlock{{Type: "text", Text: "done"}}}}))

	msgs := i.carrierTranscript()
	if len(msgs) != 6 {
		t.Fatalf("transcript = %d messages, want 6 (2 snapshot + user + assistant + folded tools + assistant)", len(msgs))
	}
	tools := msgs[4]
	if tools.Role != provider.RoleTool || len(tools.Content) != 2 {
		t.Fatalf("folded step message = role %q with %d blocks, want RoleTool with 2", tools.Role, len(tools.Content))
	}
	tr, ok := tools.Content[1].(provider.ToolResultBlock)
	if !ok || tr.CallID != "t2" || !tr.IsError {
		t.Fatalf("second folded block = %+v, want t2 error result", tools.Content[1])
	}

	// A fresh snapshot (compact/clear/switch) replaces everything.
	rev := i.carrierTranscriptRev()
	i.handleCarrierEvent(ctrlproto.SnapshotEvent(ctrlproto.Snapshot{
		Messages: []core.WireMessage{{Role: "user", Content: []core.WireBlock{{Type: "text", Text: "compacted"}}}},
	}))
	if got := i.carrierTranscript(); len(got) != 1 || i.carrierTranscriptRev() <= rev {
		t.Fatalf("snapshot resync: %d messages (rev %d→%d), want wholesale replace", len(got), rev, i.carrierTranscriptRev())
	}
}

// TestCarrierTranscriptKeepsImagePixels: full-form snapshots and events carry
// image Data end-to-end into the pump transcript (the renderer shows real
// pixels), and a foreign turn's user_message re-stocks the rescue stash with
// its attachments instead of dropping them.
func TestCarrierTranscriptKeepsImagePixels(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = newFakeCarrier()

	i.handleCarrierEvent(ctrlproto.SnapshotEvent(ctrlproto.Snapshot{
		Messages: []core.WireMessage{{Role: "user", Content: []core.WireBlock{
			{Type: "image", MimeType: "image/png", Data: []byte{7, 7}, Bytes: 2},
		}}},
	}))
	msgs := i.carrierTranscript()
	ib, ok := msgs[0].Content[0].(provider.ImageBlock)
	if !ok || len(ib.Data) != 2 || ib.MimeType != "image/png" {
		t.Fatalf("snapshot pixels lost: %+v", msgs[0].Content[0])
	}

	// A foreign turn (text differs from the local stash) re-stocks the
	// rescue attachments from the wire; a lean carrier's size-only blocks
	// would leave it nil, same as before.
	i.handleCarrierEvent(conv(core.WireEvent{Type: "user_message",
		Message: &core.WireMessage{Role: "user", Content: []core.WireBlock{
			{Type: "text", Text: "foreign prompt"},
			{Type: "image", MimeType: "image/jpeg", Data: []byte{9}, Bytes: 1},
		}}}))
	i.mu.Lock()
	imgs := i.carrierLastImages
	i.mu.Unlock()
	if len(imgs) != 1 || imgs[0].MimeType != "image/jpeg" || len(imgs[0].Data) != 1 {
		t.Fatalf("rescue stash = %+v, want the foreign turn's attachment", imgs)
	}
}

// TestCarrierApprovalSetting: the generic /settings surface carries the
// daemon's live approval mode, and changes (picker or shift+tab) apply through
// it; the status-bar badge follows via the cached refresh.
func TestCarrierApprovalSetting(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	i.cfg.Carrier = fc

	item, ok := findSettingsItem(i.daemonSettingsItems(), "approval")
	if !ok {
		t.Fatal("approval item should exist in carrier mode")
	}
	if item.Options[item.Choice].Value != "workspace" {
		t.Fatalf("current mode = %q, want workspace", item.Options[item.Choice].Value)
	}

	i.applyApprovalMode("ask")
	if act := recv(t, fc.surfActs, "approval set"); act.id != "settings" || act.action != "set" ||
		act.args["key"] != "approval" || act.args["value"] != "ask" {
		t.Fatalf("approval action = %+v", act)
	}

	i.refreshCarrierApprovalMode()
	deadline := time.Now().Add(2 * time.Second)
	for i.approvalModeLabel() != "workspace" {
		if time.Now().After(deadline) {
			t.Fatalf("badge = %q, want workspace", i.approvalModeLabel())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestCarrierCompactAndClearRouteToService: /compact holds the local slot
// across the service round-trip (spinner + prompt queueing) and releases it
// after; /clear goes through the service instead of wiping the agent locally.
func TestCarrierCompactAndClearRouteToService(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	fc.compactGate = make(chan struct{})
	i.cfg.Carrier = fc

	i.runCarrierCompact(context.Background())
	if !i.turns.Busy() {
		t.Fatal("carrier compact should hold the busy slot")
	}
	close(fc.compactGate)
	recv(t, fc.compacts, "compact")
	deadline := time.Now().Add(2 * time.Second)
	for i.turns.Busy() {
		if time.Now().After(deadline) {
			t.Fatal("carrier compact should release the slot when done")
		}
		time.Sleep(5 * time.Millisecond)
	}

	i.slashClear(context.Background(), nil, "")
	recv(t, fc.clears, "clear")
}

// TestSwitchCarrierSession is Stage 2's core proof: re-pointing the TUI at
// another workspace session updates the status-bar provider/model, drops the
// old session's pending dialogs (refusing them TO the old session), releases
// the old local turn slot, and kicks the pump into a fresh subscription on the
// new binding. (No agent is swapped any more — the TUI holds none.)
func TestSwitchCarrierSession(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	fc.infos = map[string]ctrlproto.SessionInfo{
		"s2": {ID: "s2", Provider: "prov2", Model: "m2", Path: "/tmp/s2.jsonl"},
	}
	i.cfg.Carrier = fc
	i.cfg.CarrierSession = "s1"

	go i.runCarrierLoop(t.Context())
	if got := recv(t, fc.subs, "initial subscription"); got != "s1" {
		t.Fatalf("first subscription = %q, want s1", got)
	}

	// Live state on s1: a busy turn and a pending permission round-trip.
	i.handleCarrierEvent(conv(core.WireEvent{Type: "turn_start", Step: 1}))
	i.handleCarrierEvent(ctrlproto.PermissionEvent(ctrlproto.PermissionRequest{CallID: "c1", Tool: "bash"}))
	if !i.turns.Busy() || !i.confirmDialog.Active() {
		t.Fatal("precondition: s1 busy with a pending permission")
	}

	if err := i.SwitchCarrierSession("s2"); err != nil {
		t.Fatalf("SwitchCarrierSession: %v", err)
	}

	if got := recv(t, fc.subs, "re-subscription"); got != "s2" {
		t.Fatalf("re-subscription = %q, want s2", got)
	}
	if got := i.carrierSession(); got != "s2" {
		t.Fatalf("carrierSession = %q, want s2", got)
	}
	i.mu.Lock()
	prov, model := i.cfg.Provider, i.cfg.Model
	i.mu.Unlock()
	if prov != "prov2" || model != "m2" {
		t.Fatalf("status-bar identity = %s/%s, want prov2/m2", prov, model)
	}
	if i.turns.Busy() {
		t.Fatal("switch should release the old session's local slot")
	}
	if i.confirmDialog.Active() {
		t.Fatal("switch should drop the old session's pending dialog")
	}
	// The dropped round-trip is refused TO THE SESSION THAT ASKED (s1), even
	// though the current binding is already s2.
	got := recv(t, fc.approves, "refusal forwarded")
	if got.sess != "s1" || got.callID != "c1" || got.d.Allow {
		t.Fatalf("refusal = %+v, want refuse c1 on s1", got)
	}
}

// TestCarrierSnapshotRestoresPendingRoundTrips: switching to a mid-turn
// session must restore its parked permission/ask dialogs from the snapshot,
// not leave the turn invisibly blocked.
func TestCarrierSnapshotRestoresPendingRoundTrips(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	i.cfg.Carrier = fc

	i.handleCarrierEvent(ctrlproto.SnapshotEvent(ctrlproto.Snapshot{
		Busy:        true,
		Permissions: []ctrlproto.PermissionRequest{{CallID: "c9", Tool: "write"}},
		Asks:        []ctrlproto.AskRequest{{AskID: "a9", Question: "which?", Options: []string{"x"}}},
	}))
	if !i.turns.Busy() {
		t.Fatal("snapshot Busy should re-arm the slot")
	}
	if !i.confirmDialog.Active() || !i.questionDialog.Active() {
		t.Fatal("snapshot should restore parked permission + ask dialogs")
	}
}

// TestCarrierEndToEndVT is the pump's end-to-end proof: a REAL Run() loop on
// the VT harness, dispatching a typed prompt through the carrier and painting
// the daemon's event stream — typed keys → Prompt on the service → wire
// deltas → typewriter → screen, with busy released by the definitive done.
func TestCarrierEndToEndVT(t *testing.T) {
	fc := newFakeCarrier()
	fc.stream = make(chan ctrlproto.Event, 64)
	// There is no agent in the TUI at all (plan 4.1). The transcript renders
	// entirely from the pump-owned wire reconstruction, and this test proves it:
	// every rendered row below arrives as a wire event, with nothing local to
	// fall back on.
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true // a bound session with a credential
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
	})

	h.term.Type("hello wire\r")
	if got := recv(t, fc.prompts, "dispatched prompt"); got != "hello wire" {
		t.Fatalf("dispatched prompt = %q", got)
	}

	// Play the daemon: the prompt echoes back as a user_message (that echo IS
	// what puts the user's row in the transcript now), then the turn streams.
	fc.stream <- conv(core.WireEvent{Type: "turn_start", Step: 1})
	fc.stream <- conv(core.WireEvent{
		Type: "user_message",
		Message: &core.WireMessage{Role: "user", Content: []core.WireBlock{
			{Type: "text", Text: "hello wire"},
		}},
	})
	fc.stream <- conv(core.WireEvent{Type: "assistant_start"})
	fc.stream <- conv(core.WireEvent{Type: "text_delta", Delta: "hi from the wire"})
	h.waitText("hi from the wire")

	// Finalize: the full message + done retire the stream.
	fc.stream <- conv(core.WireEvent{
		Type: "assistant_message",
		Message: &core.WireMessage{Role: "assistant", Content: []core.WireBlock{
			{Type: "text", Text: "hi from the wire"},
		}},
	})
	fc.stream <- conv(core.WireEvent{Type: "done"})

	deadline := time.Now().Add(2 * time.Second)
	for h.i.turns.Busy() {
		if time.Now().After(deadline) {
			t.Fatal("done should release the busy slot")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Both rows must be on screen from the wire-reconstructed transcript, with
	// no agent anywhere in the TUI to have supplied them.
	h.waitText("hello wire")
	h.waitText("hi from the wire")
}

// TestCarrierNewSessionVT drives /new end-to-end on the VT harness with the
// carrier-backed NewSession closure (the shape the ctrlproto entry point
// wires): the switch rebinds the TUI to the created session and resets to a
// fresh transcript. No agent is installed — the TUI holds none (plan 4.1).
func TestCarrierNewSessionVT(t *testing.T) {
	fc := newFakeCarrier()
	fc.stream = make(chan ctrlproto.Event, 16)
	var ivp *Interactive
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true // a bound session with a credential
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
		cfg.NewSession = func(_, _ string) error {
			return ivp.SwitchCarrierSession("s2")
		}
	})
	ivp = h.i

	h.term.Type("/new\r")
	h.waitText(i18n.T("started a new session"))
	if got := h.i.carrierSession(); got != "s2" {
		t.Fatalf("carrierSession after /new = %q, want s2", got)
	}
}

// TestCarrierAskInversion mirrors the permission test for the Asker seam.
func TestCarrierAskInversion(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	i.cfg.Carrier = fc

	i.handleCarrierEvent(ctrlproto.AskEvent(ctrlproto.AskRequest{
		AskID: "ask_1", Question: "which?", Options: []string{"a", "b"},
	}))
	if !i.questionDialog.Active() {
		t.Fatal("ask request should open the question dialog")
	}
	i.questionDialog.HandleKey(tui.Key{Kind: tui.KeyEnter}) // pick "a"
	got := recv(t, fc.answers, "answer")
	if got.askID != "ask_1" || got.a.Answer != "a" {
		t.Fatalf("answer = %+v", got)
	}

	i.handleCarrierEvent(ctrlproto.AskEvent(ctrlproto.AskRequest{AskID: "ask_2", Question: "again?", Options: []string{"x"}}))
	i.handleCarrierEvent(ctrlproto.AskResolvedEvent("ask_2"))
	if i.questionDialog.Active() {
		t.Fatal("resolved event should dismiss the pending ask")
	}
}

// TestHandleWireEventStreamsText: the wire text_delta path must paint through
// the same pacer the typed path uses, so the transcript reads identically.
func TestHandleWireEventStreamsText(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	i.handleWireEvent(conv(core.WireEvent{Type: "assistant_start"}))
	i.handleWireEvent(conv(core.WireEvent{Type: "text_delta", Delta: "hello "}))
	i.handleWireEvent(conv(core.WireEvent{Type: "text_delta", Delta: "world"}))

	// Drain the pacer to paint everything buffered.
	i.turns.mu.Lock()
	i.turns.stream.paceTick(1000)
	got := i.turns.stream.visible()
	i.turns.mu.Unlock()
	if got != "hello world" {
		t.Fatalf("streamed text = %q, want %q", got, "hello world")
	}
}

// TestHandleWireEventToolLifecycle: a tool call's start → args → call → result
// wire events must build the same ToolCallView the typed handlers do.
func TestHandleWireEventToolLifecycle(t *testing.T) {
	i := newCtrlprotoTestInteractive()

	i.handleWireEvent(conv(core.WireEvent{Type: "tool_use_start", ID: "t1", Name: "write"}))
	tc, ok := i.toolCalls["t1"]
	if !ok || tc.Name != "write" || !tc.Streaming {
		t.Fatalf("tool_use_start: %+v (ok=%v)", tc, ok)
	}

	// Streamed args: the live path must be peeled off the partial JSON.
	i.handleWireEvent(conv(core.WireEvent{Type: "tool_use_args", ID: "t1", Delta: `{"file_path":"/tmp/x.go"`}))
	if tc.LivePath != "/tmp/x.go" {
		t.Fatalf("live path = %q, want /tmp/x.go", tc.LivePath)
	}

	i.handleWireEvent(conv(core.WireEvent{Type: "tool_use_end", ID: "t1"}))
	if tc.Streaming {
		t.Fatal("tool_use_end should clear Streaming")
	}

	i.handleWireEvent(conv(core.WireEvent{Type: "tool_call", ID: "t1", Name: "write", Args: json.RawMessage(`{"file_path":"/tmp/x.go"}`)}))
	if tc.Args == "" {
		t.Fatal("tool_call should set a rendered Args summary")
	}

	i.handleWireEvent(conv(core.WireEvent{Type: "tool_progress", ID: "t1", Text: "writing…"}))
	if tc.Progress != "writing…" {
		t.Fatalf("progress = %q", tc.Progress)
	}

	i.handleWireEvent(conv(core.WireEvent{
		Type: "tool_result", ID: "t1", IsError: true,
		Result: []core.WireBlock{{Type: "text", Text: "boom"}, {Type: "text", Text: "line2"}},
	}))
	if !tc.Done || !tc.Error || tc.Result != "boom\nline2" {
		t.Fatalf("tool_result: done=%v err=%v result=%q", tc.Done, tc.Error, tc.Result)
	}
	if i.editsAdded != 0 || i.editsRemoved != 0 {
		t.Fatalf("errored result must not tally edits: +%d -%d", i.editsAdded, i.editsRemoved)
	}

	// A clean mutating result carries first-class line counts off the wire
	// into the status bar's Δ segment.
	i.handleWireEvent(conv(core.WireEvent{Type: "tool_use_start", ID: "t2", Name: "edit"}))
	i.handleWireEvent(conv(core.WireEvent{
		Type: "tool_result", ID: "t2",
		Result:     []core.WireBlock{{Type: "text", Text: "diff"}},
		LinesAdded: 4, LinesRemoved: 2,
	}))
	if i.editsAdded != 4 || i.editsRemoved != 2 {
		t.Fatalf("edit stats off the wire: +%d -%d, want +4 -2", i.editsAdded, i.editsRemoved)
	}
}

// TestHandleWireEventUsageAndStatus: usage, guard rejection, and the two
// terminal turn_end stops must land in the same status/usage state.
func TestHandleWireEventUsageAndStatus(t *testing.T) {
	i := newCtrlprotoTestInteractive()

	i.handleWireEvent(conv(core.WireEvent{
		Type:       "usage",
		Usage:      &core.WireUsage{Input: 100, CacheRead: 20},
		Cumulative: &core.WireUsage{Input: 500, Output: 50, CostUSD: 0.25},
	}))
	if i.lastCtxInput != 120 {
		t.Fatalf("lastCtxInput = %d, want 120", i.lastCtxInput)
	}
	if i.cumUsage != (provider.Usage{InputTokens: 500, OutputTokens: 50, CostUSD: 0.25}) {
		t.Fatalf("cumUsage = %+v", i.cumUsage)
	}

	i.handleWireEvent(conv(core.WireEvent{Type: "user_message_rejected", Text: "blocked by guard"}))
	if i.statusErr != "blocked by guard" {
		t.Fatalf("statusErr = %q", i.statusErr)
	}

	i.handleWireEvent(conv(core.WireEvent{Type: "turn_end", Stop: string(provider.StopAborted)}))
	if i.statusOK != i18n.T("cancelled") || i.statusErr != "" {
		t.Fatalf("aborted turn_end: ok=%q err=%q", i.statusOK, i.statusErr)
	}
}

// TestCarrierExtPanelSyncAndClose: a proactively-opened extension panel
// (host-hook, surfaced daemon-side as ext:<ext>:<id>) mirrors into the TUI
// overlay on surface_updated, and closes when the daemon drops it — reported
// only via surfaces_changed (paneClose emits no per-panel event).
func TestCarrierExtPanelSyncAndClose(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	i.extPanel = dialogs.NewExtPanelDialog()
	fc := newFakeCarrier()
	i.cfg.Carrier = fc

	fc.mu.Lock()
	fc.panels = map[string]ctrlproto.Surface{
		"ext:memory:main": {ID: "ext:memory:main", Title: "Memory", Kind: "panel",
			Panel: &ctrlproto.PanelView{Ext: "memory", Lines: []string{"recent"}, Footer: "q to close"}},
	}
	fc.metas = []ctrlproto.SurfaceMeta{{ID: "ext:memory:main", Kind: "panel"}}
	fc.mu.Unlock()

	// Proactive open: surface_updated for the ext panel opens the overlay and
	// records the mirror id (so the close check knows it's daemon-backed).
	i.handleCarrierEvent(ctrlproto.SurfaceUpdatedEvent("ext:memory:main"))
	waitForCond(t, func() bool {
		i.mu.Lock()
		defer i.mu.Unlock()
		return i.extPanel.Active() && i.extPanel.Ext() == "memory" && i.extPanel.ID() == "main" &&
			i.carrierPanelSurface == "ext:memory:main"
	}, "panel overlay to open from the ext surface")

	// The extension closes it daemon-side: surfaces_changed with the panel gone
	// → the overlay drops (and the mirror id clears).
	fc.mu.Lock()
	fc.metas = nil
	delete(fc.panels, "ext:memory:main")
	fc.mu.Unlock()
	i.handleCarrierEvent(ctrlproto.SurfacesChangedEvent())
	waitForCond(t, func() bool {
		i.mu.Lock()
		defer i.mu.Unlock()
		return !i.extPanel.Active() && i.carrierPanelSurface == ""
	}, "panel overlay to close after the surface disappears")
}

func waitForCond(t *testing.T, cond func() bool, what string) {
	t.Helper()
	for range 250 {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
