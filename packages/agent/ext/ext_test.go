package ext

import (
	"bufio"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/extproto"
)

// ---------- test harness ----------

// extHarness wires an Extension to io.Pipe pairs so a test can play
// the role of the host: write host→ext frames, read ext→host frames.
// The scanner runs in a permanent background goroutine and delivers
// frames over a buffered channel, avoiding the deadlock that would
// occur if the test goroutine alternated between writing and reading
// a synchronous pipe.
type extHarness struct {
	ext    *Extension
	hostW  *io.PipeWriter // test writes here → ext reads as stdin
	frames chan rawFrame  // ext→host frames delivered here
}

type rawFrame struct {
	hdr extproto.Frame
	raw []byte
}

func newHarness(name string) *extHarness {
	extStdinR, extStdinW := io.Pipe()
	extStdoutR, extStdoutW := io.Pipe()

	e := New(name, "0.0.0-test")
	e.in = extStdinR
	e.out = extStdoutW
	e.stderr = io.Discard

	h := &extHarness{
		ext:    e,
		hostW:  extStdinW,
		frames: make(chan rawFrame, 64),
	}

	// Background reader: scan ext's stdout and push every frame into
	// the channel so the test goroutine never needs to block on the pipe.
	go func() {
		scanner := bufio.NewScanner(extStdoutR)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			b := scanner.Bytes()
			cp := make([]byte, len(b))
			copy(cp, b)
			var f extproto.Frame
			json.Unmarshal(cp, &f)
			h.frames <- rawFrame{f, cp}
		}
		close(h.frames)
	}()

	return h
}

// next returns the next frame, timing out after 2 s.
func (h *extHarness) next(t *testing.T) rawFrame {
	t.Helper()
	select {
	case f, ok := <-h.frames:
		if !ok {
			t.Fatal("frame channel closed (ext stdout EOF)")
		}
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for frame from extension")
		return rawFrame{}
	}
}

// drainUntil reads frames until one with type == want arrives.
func (h *extHarness) drainUntil(t *testing.T, want string) rawFrame {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case f, ok := <-h.frames:
			if !ok {
				t.Fatalf("frame channel closed before seeing %q", want)
			}
			if f.hdr.Type == want {
				return f
			}
		case <-deadline.C:
			t.Fatalf("timeout waiting for frame type %q", want)
			return rawFrame{}
		}
	}
}

// sendToExt writes a host→ext frame.
func (h *extHarness) sendToExt(t *testing.T, v any) {
	t.Helper()
	b, err := extproto.Encode(v)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := h.hostW.Write(b); err != nil {
		t.Fatalf("write to ext: %v", err)
	}
}

// handshake performs the hello / hello_ack exchange and drains frames
// until "ready".
func (h *extHarness) handshake(t *testing.T) {
	t.Helper()
	f := h.next(t)
	if f.hdr.Type != "hello" {
		t.Fatalf("expected hello, got %q", f.hdr.Type)
	}
	h.sendToExt(t, extproto.HelloAckFromHost{
		Type:            "hello_ack",
		ProtocolVersion: extproto.ProtocolVersion,
		TervaVersion:    "0.0.0-test",
		Provider:        "anthropic",
		Model:           "claude-test",
	})
	for {
		f := h.next(t)
		if f.hdr.Type == "ready" {
			return
		}
	}
}

// ---------- tests ----------

// TestOpenPanelEmitsCorrectFrame checks that e.OpenPanel sends a
// well-formed open_panel frame with the correct PanelSpec fields.
func TestOpenPanelEmitsCorrectFrame(t *testing.T) {
	h := newHarness("test-ext")
	go h.ext.Run()
	h.handshake(t)

	go h.ext.OpenPanel("my-panel", "My Title", []string{"line a", "line b"}, "esc close")

	f := h.drainUntil(t, "open_panel")

	var op extproto.OpenPanelFromExt
	if err := json.Unmarshal(f.raw, &op); err != nil {
		t.Fatalf("unmarshal open_panel: %v", err)
	}
	if op.Panel.ID != "my-panel" {
		t.Errorf("panel id: want %q, got %q", "my-panel", op.Panel.ID)
	}
	if op.Panel.Title != "My Title" {
		t.Errorf("panel title: want %q, got %q", "My Title", op.Panel.Title)
	}
	if len(op.Panel.Lines) != 2 || op.Panel.Lines[0] != "line a" || op.Panel.Lines[1] != "line b" {
		t.Errorf("panel lines: got %v", op.Panel.Lines)
	}
	if op.Panel.Footer != "esc close" {
		t.Errorf("panel footer: want %q, got %q", "esc close", op.Panel.Footer)
	}

	h.hostW.Close()
}

// TestSubmitSlashEmitsFrame checks that e.SubmitSlash sends a
// submit_slash frame carrying the slash text verbatim, and that a
// non-slash string emits nothing at all.
func TestSubmitSlashEmitsFrame(t *testing.T) {
	h := newHarness("test-ext")
	go h.ext.Run()
	h.handshake(t)

	// A non-slash string is a no-op on the wire. Fire it first, then a
	// real slash; the first frame that arrives must be the slash one —
	// proving the non-slash call emitted nothing.
	go func() {
		h.ext.SubmitSlash("not a slash")
		h.ext.SubmitSlash("/cd /tmp/x")
	}()

	f := h.drainUntil(t, "submit_slash")

	var sub extproto.SubmitSlashFromExt
	if err := json.Unmarshal(f.raw, &sub); err != nil {
		t.Fatalf("unmarshal submit_slash: %v", err)
	}
	if sub.Type != "submit_slash" {
		t.Errorf("type: want %q, got %q", "submit_slash", sub.Type)
	}
	if sub.Text != "/cd /tmp/x" {
		t.Errorf("text: want %q, got %q", "/cd /tmp/x", sub.Text)
	}

	// No second submit_slash should be queued behind the first: the
	// non-slash call must not have produced a frame.
	select {
	case f, ok := <-h.frames:
		if ok && f.hdr.Type == "submit_slash" {
			t.Fatalf("non-slash SubmitSlash emitted a frame: %s", f.raw)
		}
	case <-time.After(100 * time.Millisecond):
		// No extra frame — expected.
	}

	h.hostW.Close()
}

// TestBlockingToolWaitsForPanelKey is the core integration test for
// the human-in-the-loop pattern: the tool handler opens a panel,
// blocks on a channel, and only returns a tool_result after a key
// event arrives.
func TestBlockingToolWaitsForPanelKey(t *testing.T) {
	h := newHarness("gate-ext")

	const pid = "gate-panel"
	const toolCallID = "tc-001"

	approved := make(chan bool, 1)

	h.ext.OnPanelKey(pid, func(key, text string) {
		switch {
		case key == "rune" && text == "y":
			h.ext.ClosePanel(pid)
			approved <- true
		case key == "rune" && text == "n", key == "esc":
			h.ext.ClosePanel(pid)
			approved <- false
		}
	}, func() { approved <- false })

	h.ext.Tool("gate", "needs approval",
		json.RawMessage(`{"type":"object","properties":{}}`),
		func(args json.RawMessage) ToolResult {
			h.ext.OpenPanel(pid, "Approve?",
				[]string{"  y  approve", "  n  deny"}, "y/n")
			if <-approved {
				return TextResult("approved")
			}
			return TextErrorResult("denied")
		})

	go h.ext.Run()
	h.handshake(t)

	h.sendToExt(t, extproto.ToolCallFromHost{
		Type: "tool_call", ID: toolCallID, Name: "gate",
		Args: json.RawMessage(`{}`),
	})

	// Tool goroutine must open the panel before it can reply.
	h.drainUntil(t, "open_panel")

	// Send approval — tool should now unblock and emit tool_result.
	h.sendToExt(t, extproto.PanelKeyFromHost{
		Type: "panel_key", PanelID: pid, Key: "rune", Text: "y",
	})

	f := h.drainUntil(t, "tool_result")
	var tr extproto.ToolResultFromExt
	if err := json.Unmarshal(f.raw, &tr); err != nil {
		t.Fatalf("unmarshal tool_result: %v", err)
	}
	if tr.ID != toolCallID {
		t.Errorf("tool_result id: want %q, got %q", toolCallID, tr.ID)
	}
	if tr.IsError {
		t.Errorf("expected success, got is_error=true")
	}
	if len(tr.Content) == 0 || !strings.Contains(tr.Content[0].Text, "approved") {
		t.Errorf("expected 'approved' in content, got %+v", tr.Content)
	}

	h.hostW.Close()
}

// TestBlockingToolDenied mirrors TestBlockingToolWaitsForPanelKey but
// sends "n" so the tool returns an error result.
func TestBlockingToolDenied(t *testing.T) {
	h := newHarness("gate-ext-deny")

	const pid = "deny-panel"
	const toolCallID = "tc-002"

	approved := make(chan bool, 1)

	h.ext.OnPanelKey(pid, func(key, text string) {
		switch {
		case key == "rune" && text == "y":
			h.ext.ClosePanel(pid)
			approved <- true
		case key == "rune" && text == "n", key == "esc":
			h.ext.ClosePanel(pid)
			approved <- false
		}
	}, func() { approved <- false })

	h.ext.Tool("gate2", "needs approval",
		json.RawMessage(`{"type":"object","properties":{}}`),
		func(args json.RawMessage) ToolResult {
			h.ext.OpenPanel(pid, "Approve?", []string{"y/n"}, "")
			if <-approved {
				return TextResult("approved")
			}
			return TextErrorResult("denied")
		})

	go h.ext.Run()
	h.handshake(t)

	h.sendToExt(t, extproto.ToolCallFromHost{
		Type: "tool_call", ID: toolCallID, Name: "gate2",
		Args: json.RawMessage(`{}`),
	})

	h.drainUntil(t, "open_panel")

	h.sendToExt(t, extproto.PanelKeyFromHost{
		Type: "panel_key", PanelID: pid, Key: "rune", Text: "n",
	})

	f := h.drainUntil(t, "tool_result")
	var tr extproto.ToolResultFromExt
	if err := json.Unmarshal(f.raw, &tr); err != nil {
		t.Fatalf("unmarshal tool_result: %v", err)
	}
	if !tr.IsError {
		t.Errorf("expected is_error=true on denial")
	}
	if len(tr.Content) == 0 || !strings.Contains(tr.Content[0].Text, "denied") {
		t.Errorf("expected 'denied' in content, got %+v", tr.Content)
	}

	h.hostW.Close()
}

// TestOnSessionReceivesSessionIdentity covers the protocol-2 session
// surface: OnSession subscribes to session_start, fires with the
// session id/path/title, keeps Host() current before the callback runs,
// re-fires on a switch, and reports an empty id when the session closes.
func TestOnSessionReceivesSessionIdentity(t *testing.T) {
	h := newHarness("sess-ext")

	got := make(chan Session, 4)
	var hostAtCallback HostInfo
	h.ext.OnSession(func(s Session) {
		// Host() must already reflect the new session when OnSession runs.
		hostAtCallback = h.ext.Host()
		got <- s
	})

	go h.ext.Run()

	// Drive the handshake by hand so we can assert the subscribe frame
	// listed session_start (OnSession must subscribe, not just register).
	if f := h.next(t); f.hdr.Type != "hello" {
		t.Fatalf("expected hello, got %q", f.hdr.Type)
	}
	h.sendToExt(t, extproto.HelloAckFromHost{
		Type: "hello_ack", ProtocolVersion: extproto.ProtocolVersion,
		TervaVersion: "0.0.0-test", Provider: "anthropic", Model: "claude-test",
	})
	var sawSubscribe bool
	for {
		f := h.next(t)
		if f.hdr.Type == "subscribe" {
			var sub extproto.SubscribeFromExt
			if err := json.Unmarshal(f.raw, &sub); err == nil && slices.Contains(sub.Events, "session_start") {
				sawSubscribe = true
			}
		}
		if f.hdr.Type == "ready" {
			break
		}
	}
	if !sawSubscribe {
		t.Fatal("OnSession did not subscribe to session_start")
	}

	waitSession := func() Session {
		t.Helper()
		select {
		case s := <-got:
			return s
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for OnSession callback")
			return Session{}
		}
	}

	// First session opens.
	h.sendToExt(t, extproto.EventFromHost{
		Type: "event", Event: "session_start",
		SessionID: "sess-1", SessionPath: "/a.tervasession", SessionTitle: "First",
	})
	s := waitSession()
	if s.ID != "sess-1" || s.Path != "/a.tervasession" || s.Title != "First" {
		t.Errorf("first session: got %+v", s)
	}
	if hostAtCallback.SessionID != "sess-1" || hostAtCallback.SessionTitle != "First" {
		t.Errorf("Host() not current inside callback: %+v", hostAtCallback)
	}
	if h.ext.Host().SessionID != "sess-1" {
		t.Errorf("Host().SessionID after event: %q", h.ext.Host().SessionID)
	}

	// Switch to a different session (resume / fork / new).
	h.sendToExt(t, extproto.EventFromHost{
		Type: "event", Event: "session_start",
		SessionID: "sess-2", SessionPath: "/b.tervasession", SessionTitle: "Second",
	})
	if s := waitSession(); s.ID != "sess-2" || s.Title != "Second" {
		t.Errorf("switched session: got %+v", s)
	}

	// Session closes / --no-session: empty id.
	h.sendToExt(t, extproto.EventFromHost{Type: "event", Event: "session_start"})
	if s := waitSession(); s.ID != "" {
		t.Errorf("closed session should have empty id, got %+v", s)
	}
	if h.ext.Host().SessionID != "" {
		t.Errorf("Host().SessionID after close: %q", h.ext.Host().SessionID)
	}

	h.hostW.Close()
}

// TestSessionStartCarriesCWDAndProject: session_start refreshes the
// working directory and its project key on the Session + Host(), so an
// extension follows a /cd instead of going stale; a no-session start
// (empty cwd) keeps the last known cwd rather than blanking it.
func TestSessionStartCarriesCWDAndProject(t *testing.T) {
	h := newHarness("cwd-ext")

	got := make(chan Session, 4)
	h.ext.OnSession(func(s Session) { got <- s })

	go h.ext.Run()

	if f := h.next(t); f.hdr.Type != "hello" {
		t.Fatalf("expected hello, got %q", f.hdr.Type)
	}
	h.sendToExt(t, extproto.HelloAckFromHost{
		Type: "hello_ack", ProtocolVersion: extproto.ProtocolVersion,
		TervaVersion: "0.0.0-test", Provider: "anthropic", Model: "claude-test",
		CWD: "/launch/dir",
	})
	for {
		if h.next(t).hdr.Type == "ready" {
			break
		}
	}
	waitSession := func() Session {
		t.Helper()
		select {
		case s := <-got:
			return s
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for OnSession")
			return Session{}
		}
	}

	// session_start with a cwd: Session + Host() reflect it.
	h.sendToExt(t, extproto.EventFromHost{
		Type: "event", Event: "session_start",
		SessionID: "s1", CWD: "/work/repo", ProjectID: "work-repo-abc123",
	})
	s := waitSession()
	if s.CWD != "/work/repo" || s.ProjectID != "work-repo-abc123" {
		t.Fatalf("session cwd/project not delivered: %+v", s)
	}
	if h.ext.Host().CWD != "/work/repo" || h.ext.Host().ProjectID != "work-repo-abc123" {
		t.Fatalf("Host() did not follow the cwd: %+v", h.ext.Host())
	}

	// A no-session start (empty cwd) must NOT blank the last known cwd —
	// closing a session doesn't move the working directory.
	h.sendToExt(t, extproto.EventFromHost{Type: "event", Event: "session_start"})
	if s := waitSession(); s.ID != "" {
		t.Fatalf("expected empty session id on close, got %+v", s)
	}
	if h.ext.Host().CWD != "/work/repo" {
		t.Errorf("Host().CWD should persist across a no-session start, got %q", h.ext.Host().CWD)
	}

	h.hostW.Close()
}

// TestToolReadOnlyOption: a tool registered with ReadOnly() emits
// read_only:true on its register_tool frame; a plain tool does not, so
// the host can admit the read-only one without a permission prompt.
func TestToolReadOnlyOption(t *testing.T) {
	h := newHarness("ro-ext")
	schema := json.RawMessage(`{"type":"object"}`)
	h.ext.Tool("look", "read-only", schema, func(json.RawMessage) ToolResult { return ToolResult{} }, ReadOnly())
	h.ext.Tool("touch", "side-effecting", schema, func(json.RawMessage) ToolResult { return ToolResult{} })

	go h.ext.Run()

	if f := h.next(t); f.hdr.Type != "hello" {
		t.Fatalf("expected hello, got %q", f.hdr.Type)
	}
	h.sendToExt(t, extproto.HelloAckFromHost{
		Type: "hello_ack", ProtocolVersion: extproto.ProtocolVersion,
		TervaVersion: "0.0.0-test", Provider: "anthropic", Model: "claude-test",
	})

	readOnly := map[string]bool{}
	for {
		f := h.next(t)
		if f.hdr.Type == "register_tool" {
			var rt extproto.RegisterToolFromExt
			if err := json.Unmarshal(f.raw, &rt); err != nil {
				t.Fatalf("unmarshal register_tool: %v", err)
			}
			readOnly[rt.Name] = rt.ReadOnly
		}
		if f.hdr.Type == "ready" {
			break
		}
	}

	if !readOnly["look"] {
		t.Error("ReadOnly() tool should register with read_only:true")
	}
	if readOnly["touch"] {
		t.Error("plain tool must not be read_only")
	}

	h.hostW.Close()
}

// An oversized inbound frame (larger than the read cap) must be skipped,
// not fatal: Run keeps going and the next valid frame is still handled.
func TestSDKRunSurvivesOversizedInboundFrame(t *testing.T) {
	h := newHarness("survivor")
	got := make(chan Session, 1)
	h.ext.OnSession(func(s Session) { got <- s })

	go h.ext.Run()
	h.handshake(t)

	// Write a single raw line larger than MaxFrameBytes directly (bypass
	// the frame encoder). The old bufio.Scanner would die here.
	oversized := append([]byte(strings.Repeat("x", extproto.MaxFrameBytes+10)), '\n')
	if _, err := h.hostW.Write(oversized); err != nil {
		t.Fatalf("write oversized: %v", err)
	}

	// A valid frame after it must still be handled — proof Run survived.
	h.sendToExt(t, extproto.EventFromHost{
		Type: "event", Event: "session_start", SessionID: "sess-after",
	})
	select {
	case s := <-got:
		if s.ID != "sess-after" {
			t.Errorf("post-oversized session id = %q, want sess-after", s.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not process the frame after the oversized one — it likely died")
	}

	h.hostW.Close()
}

// TestContextContributionFrames covers the SDK context surface: a static
// register_context frame is flushed during the register handshake, and
// the dynamic card/status methods emit the right frames on demand.
func TestContextContributionFrames(t *testing.T) {
	h := newHarness("ctx-ext")
	h.ext.ContributeContext("keep exactly one task active")

	go h.ext.Run()

	// hello → hello_ack, then drain to ready, capturing register_context.
	if f := h.next(t); f.hdr.Type != "hello" {
		t.Fatalf("expected hello, got %q", f.hdr.Type)
	}
	h.sendToExt(t, extproto.HelloAckFromHost{
		Type: "hello_ack", ProtocolVersion: extproto.ProtocolVersion,
		TervaVersion: "0.0.0-test", Provider: "anthropic", Model: "claude-test",
	})
	var staticText string
	for {
		f := h.next(t)
		if f.hdr.Type == "register_context" {
			var rc extproto.RegisterContextFromExt
			if err := json.Unmarshal(f.raw, &rc); err == nil {
				staticText = rc.Text
			}
		}
		if f.hdr.Type == "ready" {
			break
		}
	}
	if staticText != "keep exactly one task active" {
		t.Fatalf("register_context not flushed during handshake, got %q", staticText)
	}

	// Dynamic card.
	h.ext.SetContextCard("tasks", "Tasks", "active foo\npending bar")
	f := h.drainUntil(t, "context_card")
	var card extproto.ContextCardFromExt
	if err := json.Unmarshal(f.raw, &card); err != nil {
		t.Fatalf("unmarshal context_card: %v", err)
	}
	if card.ID != "tasks" || card.Label != "Tasks" || !strings.Contains(card.Text, "active foo") {
		t.Errorf("context_card fields: %+v", card)
	}

	// Status segment.
	h.ext.SetStatus("tasks", "▸ foo (0/2)")
	fs := h.drainUntil(t, "status_segment")
	var seg extproto.StatusSegmentFromExt
	if err := json.Unmarshal(fs.raw, &seg); err != nil {
		t.Fatalf("unmarshal status_segment: %v", err)
	}
	if seg.ID != "tasks" || seg.Text != "▸ foo (0/2)" {
		t.Errorf("status_segment fields: %+v", seg)
	}

	// Clear.
	h.ext.ClearContextCard("tasks")
	fc := h.drainUntil(t, "context_card_clear")
	var clr extproto.ContextCardClearFromExt
	if err := json.Unmarshal(fc.raw, &clr); err != nil || clr.ID != "tasks" {
		t.Fatalf("context_card_clear: %+v (err=%v)", clr, err)
	}

	h.hostW.Close()
}
