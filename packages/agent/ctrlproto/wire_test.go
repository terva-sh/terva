package ctrlproto

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
)

var updateGolden = flag.Bool("update", false, "update golden frame files under testdata/")

type goldenCase struct {
	name  string
	frame Frame
}

func goldenCases(t *testing.T) []goldenCase {
	t.Helper()
	return []goldenCase{
		{"hello_client", HelloFrame(Hello{
			Role: RoleClient, Protocol: Protocol, Agent: "terva-web-pwa", Version: "0.1.0",
			Groups:   []Group{GroupConversation, GroupSession},
			Features: []string{FeatureImages, FeatureResolveEvents},
		})},
		{"hello_server", HelloFrame(ServerHello("terva web", "1.2.3"))},
		{"hello_server_workspace", HelloFrame(func() Hello {
			// The composition root decorates the base server hello with
			// workspace facts, the way web_mode does.
			h := ServerHello("terva web", "1.2.3")
			h.Locale = "de"
			h.CWD = "/home/u/project"
			h.Jailed = true
			return h
		}())},
		{"cmd_prompt", mustCmd(t, 1, "s1", MethodPrompt, PromptParams{
			Text:   "fix the failing test",
			Images: []Image{{MimeType: "image/png", Data: []byte("PNG")}},
		})},
		{"cmd_approve", mustCmd(t, 3, "s1", MethodApprove, ApproveParams{
			CallID:   "call_1",
			Decision: Decision{Allow: true, RememberTool: true},
		})},
		{"cmd_answer", mustCmd(t, 4, "s1", MethodAnswer, AnswerParams{
			AskID:  "ask_1",
			Answer: Answer{Answer: "the second one"},
		})},
		{"cmd_rename", mustCmd(t, 5, "s2", MethodSessionRename, RenameParams{Title: "refactor spike"})},
		{"cmd_model_switch", mustCmd(t, 6, "s1", MethodModelSwitch, ModelSwitchParams{Model: "claude-opus-4-8"})},
		{"cmd_context_node", mustCmd(t, 8, "s1", MethodContextNode, ContextNodeParams{ID: "tr/m5"})},
		{"resp_sessions", mustOK(t, 2, SessionsResult{Sessions: []SessionInfo{{
			ID: "s1", Title: "main", Provider: "anthropic", Model: "claude-opus-4-8",
			Messages: 12, Usage: core.WireUsage{Input: 100, Output: 50, CostUSD: 0.01}, Current: true,
			ContextTokens: 39000, ContextWindow: 1000000, Subscription: true,
		}}})},
		{"resp_err_busy", ErrFrame(7, CodeBusy, "a turn is already running")},
		{"event_text_delta", EventFrame("s1", ConversationEvent(core.WireEvent{Type: "text_delta", Delta: "Hello"}))},
		{"event_tool_call", EventFrame("s1", ConversationEvent(core.WireEvent{
			Type: "tool_call", ID: "call_1", Name: "bash", Args: json.RawMessage(`{"cmd":"ls -la"}`),
		}))},
		{"event_permission", EventFrame("s1", PermissionEvent(PermissionRequest{
			CallID: "call_1", Tool: "bash", Preview: "ls -la",
		}))},
		{"event_permission_worker", EventFrame("s1", PermissionEvent(PermissionRequest{
			CallID: "worker-wk7-1", Tool: "bash", Preview: "worker wk7: ls -la", Agent: "wk7",
		}))},
		{"event_ask", EventFrame("s1", AskEvent(AskRequest{
			AskID: "ask_1", Question: "Which approach?", Options: []string{"rewrite", "patch"}, AllowCustom: true,
		}))},
		{"event_permission_resolved", EventFrame("s1", PermissionResolvedEvent("call_1"))},
		{"event_sessions_changed", EventFrame(AddrWorkspace, SessionsChangedEvent())},
		{"event_snapshot", EventFrame("s1", SnapshotEvent(Snapshot{
			Session: SessionInfo{ID: "s1", Title: "main", Provider: "anthropic", Model: "claude-opus-4-8", Messages: 2},
			Messages: []core.WireMessage{
				{Role: "user", Content: []core.WireBlock{{Type: "text", Text: "hi"}}},
				{Role: "assistant", Content: []core.WireBlock{{Type: "text", Text: "hello"}}},
			},
		}))},
		{"cmd_files_list", mustCmd(t, 10, "", MethodFilesList, FilesListParams{
			Recursive: true, RespectGitignore: true,
		})},
		{"resp_files_list", mustOK(t, 10, FilesListResult{Files: []FileEntry{
			{Path: "src", Dir: true}, {Path: "src/main.go"},
		}})},
		{"cmd_replay_control", mustCmd(t, 9, "r1", MethodReplayControl, ReplayControlParams{Action: "seek", Position: 42})},
		{"event_replay_state", EventFrame("r1", ReplayStateEvent(ReplayState{
			Playing: true, Position: 42, Total: 128, Speed: 2, Mode: "effective",
		}))},
	}
}

// TestGoldenFrames pins the exact wire bytes of every frame kind (the
// connproto-v2 golden-frame discipline). Run `go test -run Golden -update` to
// regenerate after an intentional wire change.
func TestGoldenFrames(t *testing.T) {
	for _, c := range goldenCases(t) {
		t.Run(c.name, func(t *testing.T) {
			raw, err := Encode(c.frame)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, raw, "", "  "); err != nil {
				t.Fatalf("Indent: %v", err)
			}
			got := pretty.Bytes()
			path := filepath.Join("testdata", c.name+".json")
			if *updateGolden {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update): %v", err)
			}
			if !bytes.Equal(got, bytes.TrimRight(want, "\n")) {
				t.Errorf("golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", c.name, got, want)
			}
		})
	}
}

// TestFrameCodecSymmetry ensures every frame survives an Encode→Decode→Encode
// round-trip byte-identically (catches asymmetric (un)marshaling).
func TestFrameCodecSymmetry(t *testing.T) {
	for _, c := range goldenCases(t) {
		t.Run(c.name, func(t *testing.T) {
			b1, err := Encode(c.frame)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			dec, err := Decode(b1)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			b2, err := Encode(dec)
			if err != nil {
				t.Fatalf("re-Encode: %v", err)
			}
			if !bytes.Equal(b1, b2) {
				t.Errorf("codec asymmetric\n first: %s\nsecond: %s", b1, b2)
			}
		})
	}
}

// SessionInfo.Live/.Busy ride the wire so a board can subscribe only to live
// sessions and show busy markers; both omitempty, so an old daemon sends
// nothing and a client reads absence as unknown rather than idle/cold.
func TestSessionInfoLiveBusyWire(t *testing.T) {
	live, err := json.Marshal(SessionInfo{ID: "s1", Live: true, Busy: true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(live, []byte(`"live":true`)) || !bytes.Contains(live, []byte(`"busy":true`)) {
		t.Errorf("Live/Busy not carried on the wire: %s", live)
	}
	cold, err := json.Marshal(SessionInfo{ID: "s2"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cold, []byte("live")) || bytes.Contains(cold, []byte("busy")) {
		t.Errorf("a cold/unknown SessionInfo must omit live/busy (omitempty): %s", cold)
	}
}

// TaskInfo.Backend rides the wire so a board's swarm lane can distinguish a
// foreign worker from a native child; empty stays off the wire (omitempty) and
// a client reads its absence as backend-unknown. R2 of the orchestration
// reconciliation (docs/reviews/2026-07-15-eaw-review-from-orchestration-frontend.md).
func TestTaskInfoBackendWire(t *testing.T) {
	withBackend, err := json.Marshal(TaskInfo{ID: "wk1", Task: "x", Status: "running", Backend: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(withBackend, []byte(`"backend":"claude"`)) {
		t.Errorf("TaskInfo.Backend not carried on the wire: %s", withBackend)
	}
	native, err := json.Marshal(TaskInfo{ID: "a", Task: "x", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(native, []byte("backend")) {
		t.Errorf("empty Backend must be omitted (omitempty), else native children read as a backend: %s", native)
	}
}

func TestNegotiate(t *testing.T) {
	server := ServerHello("terva", "1") // all three groups + all features
	client := Hello{
		Role: RoleClient, Protocol: Protocol,
		Groups:   []Group{GroupConversation, GroupSession},
		Features: []string{FeatureImages},
	}
	c := Negotiate(server, client)

	if !c.Has(GroupConversation) || !c.Has(GroupSession) {
		t.Errorf("expected conversation+session negotiated, got %v", c.Groups)
	}
	if c.Has(GroupControl) {
		t.Errorf("control must not be negotiated when the client omits it: %v", c.Groups)
	}
	if !c.HasFeature(FeatureImages) {
		t.Error("images feature should be negotiated")
	}
	if c.HasFeature(FeatureResolveEvents) {
		t.Error("resolve-events must not be negotiated when the client omits it")
	}
	if c.Protocol != Protocol {
		t.Errorf("protocol = %d, want %d", c.Protocol, Protocol)
	}

	// The lower protocol version wins.
	older := client
	older.Protocol = Protocol - 1
	if got := Negotiate(server, older).Protocol; got != Protocol-1 {
		t.Errorf("protocol min = %d, want %d", got, Protocol-1)
	}
}

// TestServeConnRoundTrip drives ServeConn end-to-end over an in-memory carrier
// against a fake service: handshake, subscribe, prompt fan-out (including a
// permission request), and approve.
func TestServeConnRoundTrip(t *testing.T) {
	svc := newFakeSvc()
	client, server := newMemPair()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	served := make(chan Contract, 1)
	go func() {
		c, _ := ServeConn(ctx, server, svc, ServerHello("terva-test", "0"))
		served <- c
	}()

	// Handshake: client hello → server hello.
	push(t, client, HelloFrame(Hello{
		Role: RoleClient, Protocol: Protocol,
		Groups: []Group{GroupConversation, GroupSession},
	}))
	if sh := pull(t, client); sh.Kind != KindHello || sh.Hello == nil || sh.Hello.Role != RoleServer {
		t.Fatalf("expected server hello, got %+v", sh)
	}

	// Subscribe to s1.
	push(t, client, mustCmd(t, 1, "s1", MethodSubscribe, nil))
	if r := pull(t, client); r.Kind != KindResp || r.ID != 1 || r.Error != nil {
		t.Fatalf("subscribe resp: %+v", r)
	}

	// Prompt → expect an ok resp plus streamed events (order between the resp
	// and the events is not guaranteed since they share the write mutex).
	push(t, client, mustCmd(t, 2, "s1", MethodPrompt, PromptParams{Text: "hi"}))

	var (
		gotResp2   bool
		gotDelta   bool
		gotPerm    bool
		gotTurnEnd bool
		permCallID string
	)
	deadline := time.After(3 * time.Second)
	for !(gotResp2 && gotDelta && gotPerm && gotTurnEnd) {
		select {
		case <-deadline:
			t.Fatalf("timeout; resp2=%v delta=%v perm=%v end=%v", gotResp2, gotDelta, gotPerm, gotTurnEnd)
		default:
		}
		f := pull(t, client)
		switch {
		case f.Kind == KindResp && f.ID == 2:
			if f.Error != nil {
				t.Fatalf("prompt failed: %v", f.Error)
			}
			gotResp2 = true
		case f.Kind == KindEvent && f.Event != nil:
			switch f.Event.Type {
			case "text_delta":
				gotDelta = true
			case EventPermissionRequest:
				gotPerm = true
				if f.Event.Permission == nil {
					t.Fatal("permission event missing payload")
				}
				permCallID = f.Event.Permission.CallID
			case "turn_end":
				gotTurnEnd = true
			}
		}
	}

	// Approve the parked call.
	push(t, client, mustCmd(t, 3, "s1", MethodApprove, ApproveParams{CallID: permCallID, Decision: Decision{Allow: true}}))
	// Drain until we see the approve resp.
	for {
		f := pull(t, client)
		if f.Kind == KindResp && f.ID == 3 {
			if f.Error != nil {
				t.Fatalf("approve failed: %v", f.Error)
			}
			break
		}
	}
	svc.mu.Lock()
	approved := append([]string(nil), svc.approvals...)
	svc.mu.Unlock()
	if len(approved) != 1 || approved[0] != permCallID {
		t.Fatalf("expected approval of %q, got %v", permCallID, approved)
	}

	cancel()
	select {
	case c := <-served:
		if !c.Has(GroupConversation) {
			t.Errorf("negotiated contract missing conversation: %v", c.Groups)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServeConn did not return after cancel")
	}
}

// TestServeConnRejectsNonHelloFirst verifies the handshake contract.
func TestServeConnRejectsNonHelloFirst(t *testing.T) {
	svc := newFakeSvc()
	client, server := newMemPair()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errc := make(chan error, 1)
	go func() {
		_, err := ServeConn(ctx, server, svc, ServerHello("t", "0"))
		errc <- err
	}()

	push(t, client, mustCmd(t, 1, "s1", MethodPrompt, PromptParams{Text: "no hello first"}))
	// Server should reply with a bad_request error frame and then return.
	if r := pull(t, client); r.Error == nil || r.Error.Code != CodeBadRequest {
		t.Fatalf("expected bad_request, got %+v", r)
	}
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("expected ServeConn to return an error on missing hello")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServeConn did not return")
	}
}

// fakeReplaySvc is a WorkspaceService that also serves the replay group.
type fakeReplaySvc struct {
	*fakeSvc
	mu    sync.Mutex
	state ReplayState
}

func (f *fakeReplaySvc) ReplayControl(ctx context.Context, sess string, p ReplayControlParams) (ReplayState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch p.Action {
	case "seek":
		f.state.Position = p.Position
	case "play":
		f.state.Playing = true
	case "pause":
		f.state.Playing = false
	}
	return f.state, nil
}

func (f *fakeReplaySvc) ReplayState(ctx context.Context, sess string) (ReplayState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, nil
}

// TestServeConnReplayGroup drives replay.control over the wire against a
// ReplayController-backed service and reads back the new transport state.
func TestServeConnReplayGroup(t *testing.T) {
	svc := &fakeReplaySvc{fakeSvc: newFakeSvc()}
	client, server := newMemPair()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hello := ServerHello("terva-test", "0")
	hello.Groups = append(hello.Groups, GroupReplay)
	go ServeConn(ctx, server, svc, hello)

	push(t, client, HelloFrame(Hello{
		Role: RoleClient, Protocol: Protocol,
		Groups: []Group{GroupConversation, GroupReplay},
	}))
	if sh := pull(t, client); sh.Kind != KindHello || sh.Hello == nil {
		t.Fatalf("expected server hello, got %+v", sh)
	}

	push(t, client, mustCmd(t, 1, "r1", MethodReplayControl, ReplayControlParams{Action: "seek", Position: 7}))
	r := pull(t, client)
	if r.Kind != KindResp || r.ID != 1 || r.Error != nil {
		t.Fatalf("replay.control resp: %+v", r)
	}
	var res ReplayStateResult
	if err := r.BindResult(&res); err != nil {
		t.Fatalf("BindResult: %v", err)
	}
	if res.State.Position != 7 {
		t.Errorf("state position = %d, want 7", res.State.Position)
	}
}

// TestServeConnReplayUnsupported verifies a service that is not a
// ReplayController rejects replay.* with CodeUnsupported even when the group is
// negotiated (the type-assert fallback).
func TestServeConnReplayUnsupported(t *testing.T) {
	svc := newFakeSvc() // not a ReplayController
	client, server := newMemPair()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hello := ServerHello("terva-test", "0")
	hello.Groups = append(hello.Groups, GroupReplay)
	go ServeConn(ctx, server, svc, hello)

	push(t, client, HelloFrame(Hello{Role: RoleClient, Protocol: Protocol, Groups: []Group{GroupReplay}}))
	pull(t, client) // server hello
	push(t, client, mustCmd(t, 1, "r1", MethodReplayControl, ReplayControlParams{Action: "play"}))
	if r := pull(t, client); r.Error == nil || r.Error.Code != CodeUnsupported {
		t.Fatalf("expected CodeUnsupported, got %+v", r)
	}
}

// --- helpers ---

func mustCmd(t *testing.T, id uint64, sess string, m Method, params any) Frame {
	t.Helper()
	f, err := CmdFrame(id, sess, m, params)
	if err != nil {
		t.Fatalf("CmdFrame: %v", err)
	}
	return f
}

func mustOK(t *testing.T, id uint64, result any) Frame {
	t.Helper()
	f, err := OKFrame(id, result)
	if err != nil {
		t.Fatalf("OKFrame: %v", err)
	}
	return f
}

func push(t *testing.T, c *memConn, f Frame) {
	t.Helper()
	if err := c.WriteFrame(context.Background(), f); err != nil {
		t.Fatalf("push: %v", err)
	}
}

func pull(t *testing.T, c *memConn) Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	f, err := c.ReadFrame(ctx)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	return f
}

// memConn is an in-memory FrameConn. Writes are routed through Encode/Decode so
// the round-trip test exercises the real codec.
type memConn struct {
	recv <-chan Frame
	send chan<- Frame
	done chan struct{}
	once sync.Once
}

func newMemPair() (client, server *memConn) {
	a2b := make(chan Frame, 64)
	b2a := make(chan Frame, 64)
	client = &memConn{recv: b2a, send: a2b, done: make(chan struct{})}
	server = &memConn{recv: a2b, send: b2a, done: make(chan struct{})}
	return client, server
}

func (c *memConn) ReadFrame(ctx context.Context) (Frame, error) {
	select {
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	case <-c.done:
		return Frame{}, io.EOF
	case f, ok := <-c.recv:
		if !ok {
			return Frame{}, io.EOF
		}
		return f, nil
	}
}

func (c *memConn) WriteFrame(ctx context.Context, f Frame) error {
	raw, err := Encode(f)
	if err != nil {
		return err
	}
	g, err := Decode(raw)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return io.ErrClosedPipe
	case c.send <- g:
		return nil
	}
}

func (c *memConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return nil
}

// fakeSvc is a minimal WorkspaceService for transport tests.
type fakeSvc struct {
	mu           sync.Mutex
	subs         map[string][]chan Event
	approvals    []string
	answers      []string
	defaultModel string // "provider/model@scope", set by SetDefaultModel
}

func newFakeSvc() *fakeSvc { return &fakeSvc{subs: map[string][]chan Event{}} }

func (f *fakeSvc) broadcast(sess string, ev Event) {
	f.mu.Lock()
	chans := append([]chan Event(nil), f.subs[sess]...)
	f.mu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (f *fakeSvc) AuthProviders(ctx context.Context) (ProvidersView, error) {
	return ProvidersView{Providers: []ProviderInfo{{ID: "anthropic", Label: "Anthropic", Method: "oauth"}}}, nil
}

func (f *fakeSvc) Subscribe(ctx context.Context, sess string) (<-chan Event, error) {
	ch := make(chan Event, 32)
	f.mu.Lock()
	f.subs[sess] = append(f.subs[sess], ch)
	f.mu.Unlock()
	go func() {
		<-ctx.Done()
		f.mu.Lock()
		cur := f.subs[sess][:0]
		for _, c := range f.subs[sess] {
			if c != ch {
				cur = append(cur, c)
			}
		}
		f.subs[sess] = cur
		f.mu.Unlock()
	}()
	return ch, nil
}

func (f *fakeSvc) Prompt(ctx context.Context, sess, text string, images []Image) error {
	f.broadcast(sess, ConversationEvent(core.WireEvent{Type: "text_delta", Delta: "hello " + text}))
	f.broadcast(sess, PermissionEvent(PermissionRequest{CallID: "call_1", Tool: "bash", Preview: "ls -la"}))
	f.broadcast(sess, ConversationEvent(core.WireEvent{Type: "turn_end", Stop: "end_turn"}))
	return nil
}

func (f *fakeSvc) Queue(ctx context.Context, sess, text string) error          { return nil }
func (f *fakeSvc) SetQueue(ctx context.Context, sess string, t []string) error { return nil }
func (f *fakeSvc) Cancel(ctx context.Context, sess string) error               { return nil }
func (f *fakeSvc) Compact(ctx context.Context, sess string) error              { return nil }
func (f *fakeSvc) Clear(ctx context.Context, sess string) error                { return nil }
func (f *fakeSvc) EditMessage(ctx context.Context, sess string, epoch uint64, index int, text string) error {
	return nil
}
func (f *fakeSvc) DeleteMessage(ctx context.Context, sess string, epoch uint64, index int) error {
	return nil
}
func (f *fakeSvc) SwipeTurn(ctx context.Context, sess string, epoch uint64, variant int) error {
	return nil
}

func (f *fakeSvc) SwipeMessage(ctx context.Context, sess string, epoch uint64, index, variant int) error {
	return nil
}
func (f *fakeSvc) RetryTurn(ctx context.Context, sess string, epoch uint64) error {
	return nil
}
func (f *fakeSvc) SideChatOpen(ctx context.Context, sess string) (string, error) {
	return "sc1", nil
}
func (f *fakeSvc) SideChatAsk(ctx context.Context, sess, id string, prior []SideChatTurn, q string) (string, error) {
	return "reply to " + q, nil
}
func (f *fakeSvc) SideChatClose(ctx context.Context, sess, id string) error { return nil }
func (f *fakeSvc) Catalog(ctx context.Context, lang string) (CatalogView, error) {
	return CatalogView{Lang: lang, Singular: map[string]string{"Send": "TX"}}, nil
}
func (f *fakeSvc) ListFiles(ctx context.Context, opts FilesListParams) (FilesListResult, error) {
	return FilesListResult{Files: []FileEntry{{Path: "src", Dir: true}, {Path: "src/main.go"}}}, nil
}

func (f *fakeSvc) Approve(ctx context.Context, sess, callID string, d core.ConfirmDecision) error {
	f.mu.Lock()
	f.approvals = append(f.approvals, callID)
	f.mu.Unlock()
	f.broadcast(sess, PermissionResolvedEvent(callID))
	return nil
}

func (f *fakeSvc) Answer(ctx context.Context, sess, askID string, a core.UserAnswer) error {
	f.mu.Lock()
	f.answers = append(f.answers, askID)
	f.mu.Unlock()
	f.broadcast(sess, AskResolvedEvent(askID))
	return nil
}

func (f *fakeSvc) Sessions(ctx context.Context) ([]SessionInfo, error) {
	return []SessionInfo{{ID: "s1", Title: "main", Current: true}}, nil
}

func (f *fakeSvc) CreateSession(ctx context.Context, opts CreateOpts) (SessionInfo, error) {
	return SessionInfo{ID: "s-new", Title: opts.Title}, nil
}

func (f *fakeSvc) ResumeSession(ctx context.Context, sess string) (SessionInfo, error) {
	return SessionInfo{ID: sess}, nil
}

func (f *fakeSvc) ForkSession(ctx context.Context, sess string, fromIndex int) (SessionInfo, error) {
	return SessionInfo{ID: "fork-of-" + sess}, nil
}

func (f *fakeSvc) RenameSession(ctx context.Context, sess, title string) error { return nil }
func (f *fakeSvc) GenerateSessionTitle(ctx context.Context, sess string) (string, error) {
	return "generated title", nil
}
func (f *fakeSvc) DeleteSession(ctx context.Context, sess string) error { return nil }

func (f *fakeSvc) Usage(ctx context.Context, sess string) (core.WireUsage, error) {
	return core.WireUsage{}, nil
}

func (f *fakeSvc) UsageSnapshot(ctx context.Context, sess string, refresh bool) (UsageInfo, error) {
	return UsageInfo{Provider: "fake", HasData: true, Refreshable: refresh}, nil
}

func (f *fakeSvc) ListResets(ctx context.Context, sess string) (ResetsListResult, error) {
	return ResetsListResult{}, nil
}

func (f *fakeSvc) ConsumeReset(ctx context.Context, sess, id string) (ResetConsumeResult, error) {
	return ResetConsumeResult{}, nil
}

func (f *fakeSvc) Context(ctx context.Context, sess string) (ContextBreakdown, error) {
	return ContextBreakdown{}, nil
}

func (f *fakeSvc) Node(ctx context.Context, sess, id, op string) (ContextNode, error) {
	return ContextNode{ID: id}, nil
}

func (f *fakeSvc) History(ctx context.Context, sess string, before, limit int, epoch uint64) (HistoryResult, error) {
	return HistoryResult{Epoch: epoch, Base: 0, Total: 0}, nil
}

func (f *fakeSvc) Reveal(ctx context.Context, sess string, ordinal int) (RevealResult, error) {
	return RevealResult{Ordinal: ordinal, PrevOrdinal: -1, Total: 1}, nil
}

func (f *fakeSvc) Surfaces(ctx context.Context, sess string) ([]SurfaceMeta, error) {
	return []SurfaceMeta{{ID: "context", Title: "Context", Kind: "context"}}, nil
}
func (f *fakeSvc) Surface(ctx context.Context, sess, id string) (Surface, error) {
	return Surface{ID: id, Kind: "context", Context: &ContextBreakdown{}}, nil
}
func (f *fakeSvc) SurfaceAction(ctx context.Context, sess, id, action string, args map[string]string) error {
	return nil
}

func (f *fakeSvc) Models(ctx context.Context) ([]ModelInfo, error) {
	return []ModelInfo{{ID: "claude-opus-4-8", Provider: "anthropic", Current: true}}, nil
}

func (f *fakeSvc) SwitchModel(ctx context.Context, sess, providerName, modelID string) error {
	return nil
}
func (f *fakeSvc) SetFavoriteModel(ctx context.Context, provider, model string, on bool) error {
	return nil
}
func (f *fakeSvc) SetDefaultModel(ctx context.Context, provider, model string, scope DefaultScope) error {
	f.defaultModel = provider + "/" + model + "@" + string(scope)
	return nil
}
func (f *fakeSvc) Trust(ctx context.Context, parent bool) error { return nil }
func (f *fakeSvc) Untrust(ctx context.Context) error            { return nil }
func (f *fakeSvc) Restart(ctx context.Context) error            { return nil }

// Ensure the fake satisfies the interface.
var _ WorkspaceService = (*fakeSvc)(nil)
