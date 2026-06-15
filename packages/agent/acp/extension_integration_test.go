//go:build terva_acp

package acp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// The extstub lives in this package's testdata; build it once per test binary,
// mirroring mcp_integration_test.go's buildMCPStub. A compiled Go binary gives
// the extension a deterministic, interpreter-free protocol implementation.
var (
	extStubOnce sync.Once
	extStubPath string
	extStubErr  error
)

func buildExtStub(t *testing.T) string {
	t.Helper()
	extStubOnce.Do(func() {
		dir, err := os.MkdirTemp("", "acp-extstub")
		if err != nil {
			extStubErr = err
			return
		}
		out := filepath.Join(dir, "extstub")
		if runtime.GOOS == "windows" {
			out += ".exe"
		}
		_, thisFile, _, _ := runtime.Caller(0)
		src := filepath.Join(filepath.Dir(thisFile), "testdata", "cmd", "extstub")
		cmd := exec.Command("go", "build", "-o", out, src)
		// Hermetic build across machines; pass the runner env so go build finds
		// HOME/PATH/GOCACHE.
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, berr := cmd.CombinedOutput(); berr != nil {
			extStubErr = berr
			_ = b
			return
		}
		extStubPath = out
	})
	if extStubErr != nil {
		t.Fatalf("build extstub: %v", extStubErr)
	}
	return extStubPath
}

// installFakeExtension writes the fake extension into root/extensions/webext:
// an extension.json that execs the extstub binary and declares the writer
// tool's `ask` permission, so the real buildPermissionPolicy-style manifest
// path compiles it into the gate. Returns root (the temp TERVA_HOME for the
// session's extensions.Manager).
func installFakeExtension(t *testing.T) string {
	t.Helper()
	stub := buildExtStub(t)
	root := t.TempDir()
	extDir := filepath.Join(root, "extensions", "webext")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"name":    "webext",
		"version": "0.0.1",
		"exec":    stub,
		"permissions": []map[string]any{
			{"tool": "writer_tool", "decision": "ask", "reason": "writes a file into the workspace"},
		},
	}
	mfb, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(extDir, "extension.json"), mfb, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// serveExt spins up Serve over an io.Pipe pair with the given factory and runs
// initialize. Returns the harness + a teardown. Shared by the extension tests;
// each drives session/new itself so it can assert the registry/cleanup.
func serveExt(t *testing.T, factory AgentFactory) (*harness, func(), func()) {
	t.Helper()
	caR, caW := io.Pipe()
	acR, acW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- Serve(ctx, caR, acW, factory, AgentInfo{Name: "terva", Version: "test"}) }()

	h := newHarness(t, caW, acR)
	h.call(MethodInitialize, map[string]any{"protocolVersion": 1})

	teardown := func() {
		cancel()
		_ = caW.Close()
		_ = acW.Close()
		_ = caR.Close()
		_ = acR.Close()
		select {
		case <-serveErr:
		case <-time.After(2 * time.Second):
		}
	}
	// drain pulls the available_commands_update that follows session/new so it
	// doesn't clog the pipe for the next read (mirrors the harness helpers).
	drain := func() { _ = h.expectUpdate() }
	return h, drain, teardown
}

// TestACPExtensionToolAvailableAndExecutes proves verification (a): a read-only
// extension tool is in the ACP agent's registry, executes when the model calls
// it, and narrates as tool_call + tool_call_update — with NO permission prompt
// in workspace mode (verification (c) for the read-only tool).
func TestACPExtensionReadOnlyToolNoPrompt(t *testing.T) {
	root := installFakeExtension(t)
	client := &multiToolClient{toolName: "reader_tool", toolArgs: `{}`, toolTurns: 1}
	factory := &fakeFactory{
		client:  client,
		tools:   core.Registry{},
		extRoot: root,
	}

	h, drain, teardown := serveExt(t, factory)
	defer teardown()

	newRes := h.call(MethodSessionNew, map[string]any{"cwd": t.TempDir()})
	sid, _ := newRes["sessionId"].(string)
	if sid == "" {
		t.Fatal("session/new returned empty sessionId")
	}
	drain()

	// (a) the extension tool is in the agent's registry.
	reg := factory.lastExtRegistry()
	if reg == nil {
		t.Fatal("no extension registry captured; the extension path did not run")
	}
	if _, ok := reg["reader_tool"]; !ok {
		t.Fatalf("reader_tool missing from the ACP agent registry; have %v", registryNames(reg))
	}
	if _, ok := reg["writer_tool"]; !ok {
		t.Fatalf("writer_tool missing from the ACP agent registry; have %v", registryNames(reg))
	}

	// (c) a read-only extension tool runs with no permission prompt in
	// workspace mode: send a normal blocking call; if a prompt were issued the
	// permHandler would fire — we pass one that fails the test if invoked.
	reqID := h.send(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "read it"}},
	})
	res := h.awaitResponse(reqID, func(string) map[string]any {
		t.Error("read-only extension tool unexpectedly triggered session/request_permission")
		return map[string]any{"outcome": PermOutcomeSelected, "optionId": PermAllowOnce}
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Fatalf("stopReason = %q; want %q", res["stopReason"], StopEndTurn)
	}

	// The tool executed and round-tripped its result onto a tool_call_update.
	var sawToolCall, sawResult bool
	for _, u := range h.drainUpdates() {
		upd, _ := u["update"].(map[string]any)
		if upd == nil {
			continue
		}
		switch upd["sessionUpdate"] {
		case UpdateToolCall:
			if upd["toolCallId"] == "call-1" {
				sawToolCall = true
			}
		case UpdateToolCallUpdate:
			if content, ok := upd["content"].([]any); ok {
				for _, ci := range content {
					cm, _ := ci.(map[string]any)
					inner, _ := cm["content"].(map[string]any)
					if text, _ := inner["text"].(string); text == "read-ok" {
						sawResult = true
					}
				}
			}
		}
	}
	if !sawToolCall {
		t.Error("no tool_call for the read-only extension tool")
	}
	if !sawResult {
		t.Error("extension tool result (read-ok) did not round-trip onto a tool_call_update")
	}
}

// TestACPExtensionWriterToolAsksPermission proves verification (b): the writer
// tool — whose manifest declares `ask` — triggers session/request_permission
// correlated to the toolCallId. Reject blocks execution (model-readable
// refusal); allow runs it. Also proves event fanout does not break translation
// (verification (d)): agent_message_chunk / tool_call / tool_call_update still
// flow while the extension event observer is active.
func TestACPExtensionWriterToolAsksPermission(t *testing.T) {
	for _, tc := range []struct {
		name     string
		optionID string
		wantRan  bool
	}{
		{"reject", PermRejectOnce, false},
		{"allow", PermAllowOnce, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := installFakeExtension(t)
			client := &multiToolClient{toolName: "writer_tool", toolArgs: `{"path":"out.txt"}`, toolTurns: 1}
			factory := &fakeFactory{
				client:  client,
				tools:   core.Registry{},
				extRoot: root,
			}
			h, drain, teardown := serveExt(t, factory)
			defer teardown()

			newRes := h.call(MethodSessionNew, map[string]any{"cwd": t.TempDir()})
			sid, _ := newRes["sessionId"].(string)
			drain()

			reqID := h.send(MethodSessionPromptName, map[string]any{
				"sessionId": sid,
				"prompt":    []map[string]any{{"type": "text", "text": "write it"}},
			})

			var correlated string
			res := h.awaitResponse(reqID, func(toolCallID string) map[string]any {
				correlated = toolCallID
				return map[string]any{"outcome": PermOutcomeSelected, "optionId": tc.optionID}
			})

			// (b) the permission request correlated to the right toolCallId.
			if correlated != "call-1" {
				t.Errorf("request_permission toolCallId = %q; want call-1 (§13 correlation)", correlated)
			}
			if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
				t.Errorf("stopReason = %v; want %q", res["stopReason"], StopEndTurn)
			}

			// (d) translation survives the extension event fanout: the
			// session/update stream still carries agent_message_chunk + the
			// tool_call, and the tool_call_update reflects the outcome.
			var sawMsgChunk, sawToolCall, sawWrote, sawFailed bool
			for _, u := range h.drainUpdates() {
				upd, _ := u["update"].(map[string]any)
				if upd == nil {
					continue
				}
				switch upd["sessionUpdate"] {
				case UpdateAgentMessageChunk:
					sawMsgChunk = true
				case UpdateToolCall:
					if upd["toolCallId"] == "call-1" {
						sawToolCall = true
					}
				case UpdateToolCallUpdate:
					if upd["status"] == ToolStatusFailed {
						sawFailed = true
					}
					if content, ok := upd["content"].([]any); ok {
						for _, ci := range content {
							cm, _ := ci.(map[string]any)
							inner, _ := cm["content"].(map[string]any)
							if text, _ := inner["text"].(string); text == "wrote:out.txt" {
								sawWrote = true
							}
						}
					}
				}
			}
			if !sawMsgChunk {
				t.Error("no agent_message_chunk (translation broken by extension fanout?)")
			}
			if !sawToolCall {
				t.Error("no tool_call for the writer extension tool")
			}

			if tc.wantRan {
				if !sawWrote {
					t.Error("allow: writer tool result (wrote:out.txt) did not round-trip — tool did not run")
				}
			} else {
				if sawWrote {
					t.Error("reject: writer tool ran despite the permission being rejected")
				}
				if !sawFailed {
					t.Error("reject: no failed tool_call_update (model should see a refusal)")
				}
			}
		})
	}
}

// TestACPExtensionCleanupOnRebind proves verification (e): reloading an open
// session id stops the superseded session's extensions.Manager (its Cleanup
// ran), so no extension subprocess leaks across a rebind.
func TestACPExtensionCleanupOnRebind(t *testing.T) {
	root := installFakeExtension(t)
	sessRoot := t.TempDir()
	cwd := t.TempDir()
	factory := &fakeFactory{
		client:  &textTurnClient{reply: "ok"},
		tools:   core.Registry{},
		extRoot: root,
		root:    sessRoot,
	}
	h, drain, teardown := serveExt(t, factory)
	defer teardown()

	newRes := h.call(MethodSessionNew, map[string]any{"cwd": cwd})
	sid, _ := newRes["sessionId"].(string)
	if sid == "" {
		t.Fatal("session/new returned empty sessionId")
	}
	drain()

	// No cleanup yet — the session is live.
	if got := atomic.LoadInt32(&factory.extCleanups); got != 0 {
		t.Fatalf("extension cleanup ran %d times before any rebind; want 0", got)
	}

	// Reload the SAME id while it is still live: bindSession must tear the
	// superseded manager down (prev.cleanup()), so the cleanup counter ticks.
	loadID := h.send(MethodSessionLoad, map[string]any{
		"sessionId":  sid,
		"cwd":        cwd,
		"mcpServers": []any{},
	})
	_ = h.awaitResponse(loadID, nil)

	// The superseded session's extension manager must have been stopped.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&factory.extCleanups) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&factory.extCleanups); got == 0 {
		t.Error("reloading a live session id did not stop the superseded extension manager (no leaked-subprocess teardown)")
	}
}

// extCommandSetup spins up Serve over an extension-backed factory, runs
// initialize + session/new, and returns the harness + sessionId + the
// post-session/new available_commands_update + a teardown. The factory's client
// is provided by the caller so a model-calling test (the prompt action) and a
// no-model test (display/insert/...) can each assert call counts.
func extCommandSetup(t *testing.T, client provider.Client) (*harness, string, map[string]any, func()) {
	t.Helper()
	root := installFakeExtension(t)
	factory := &fakeFactory{
		client:  client,
		tools:   core.Registry{},
		extRoot: root,
	}
	caR, caW := io.Pipe()
	acR, acW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- Serve(ctx, caR, acW, factory, AgentInfo{Name: "terva", Version: "test"}) }()

	h := newHarness(t, caW, acR)
	h.call(MethodInitialize, map[string]any{"protocolVersion": 1})
	newRes := h.call(MethodSessionNew, map[string]any{"cwd": t.TempDir()})
	sid, _ := newRes["sessionId"].(string)
	if sid == "" {
		t.Fatal("session/new returned empty sessionId")
	}
	catalog := h.expectUpdate() // the available_commands_update that follows session/new

	teardown := func() {
		cancel()
		_ = caW.Close()
		_ = acW.Close()
		_ = caR.Close()
		_ = acR.Close()
		select {
		case <-serveErr:
		case <-time.After(2 * time.Second):
		}
	}
	return h, sid, catalog, teardown
}

// TestACPExtensionCommandsAdvertised proves verification (a): session/new's
// available_commands_update carries the extension-registered commands alongside
// the built-in curated set, and a built-in wins a name collision (the
// extension's `help` is NOT advertised as a separate entry — /help is the
// built-in).
func TestACPExtensionCommandsAdvertised(t *testing.T) {
	_, _, catalog, teardown := extCommandSetup(t, &countingTextClient{})
	defer teardown()

	names, found := findAvailableCommands([]map[string]any{catalog})
	if !found {
		t.Fatal("session/new did not emit an available_commands_update")
	}
	// The built-in curated set is still present.
	for _, want := range []string{"clear", "compact", "help"} {
		if !names[want] {
			t.Errorf("available_commands_update missing built-in /%s", want)
		}
	}
	// The extension commands are advertised (author-defined, all of them).
	for _, want := range []string{"showinfo", "dowork", "fillin", "showpanel", "boom"} {
		if !names[want] {
			t.Errorf("available_commands_update missing extension command /%s", want)
		}
	}
	// /help is the built-in (collision); the extension's `help` must not add a
	// duplicate — there is exactly one `help` entry. We can't easily count the
	// dup from the name set alone, so assert the count directly from the
	// payload.
	if got := countCommandEntries(catalog, "help"); got != 1 {
		t.Errorf("help advertised %d times; want exactly 1 (built-in wins the collision)", got)
	}
}

// countCommandEntries counts how many AvailableCommand entries in an
// available_commands_update have the given bare name.
func countCommandEntries(update map[string]any, name string) int {
	upd, _ := update["update"].(map[string]any)
	if upd == nil || upd["sessionUpdate"] != UpdateAvailableCommands {
		return 0
	}
	cmds, _ := upd["availableCommands"].([]any)
	n := 0
	for _, ci := range cmds {
		cm, _ := ci.(map[string]any)
		if nm, _ := cm["name"].(string); nm == name {
			n++
		}
	}
	return n
}

// TestACPExtensionCommandDisplayAction proves verification (b): invoking a
// display-action extension command emits the Display text as an
// agent_message_chunk and resolves end_turn WITHOUT calling the model.
func TestACPExtensionCommandDisplayAction(t *testing.T) {
	client := &countingTextClient{}
	h, sid, _, teardown := extCommandSetup(t, client)
	defer teardown()

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/showinfo hello"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/showinfo stopReason = %q; want end_turn", res["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for a display command; want 0", got)
	}
	text := drainChunkText(h.drainUpdates())
	if !strings.Contains(text, "info:hello") {
		t.Errorf("display chunk missing the extension Display text (with the arg echoed): %q", text)
	}
}

// TestACPExtensionCommandPromptAction proves verification (c): invoking a
// prompt-action extension command runs a REAL model turn with the returned
// task text — the model IS called, the assistant reply streams as
// agent_message_chunk, and the turn resolves with the turn's own stopReason.
func TestACPExtensionCommandPromptAction(t *testing.T) {
	client := &countingTextClient{reply: "task acknowledged"}
	h, sid, _, teardown := extCommandSetup(t, client)
	defer teardown()

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/dowork ship it"}},
	})
	// The prompt action runs a normal turn, which this fake client resolves as
	// StopEnd -> end_turn.
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/dowork stopReason = %q; want end_turn (the turn's own stop reason)", res["stopReason"])
	}
	// The meaningful difference from display: the model IS called exactly once.
	if got := atomic.LoadInt32(&client.calls); got != 1 {
		t.Fatalf("model called %d times for a prompt command; want 1 (a real turn ran)", got)
	}
	text := drainChunkText(h.drainUpdates())
	if !strings.Contains(text, "task acknowledged") {
		t.Errorf("prompt action did not stream the assistant reply as an agent_message_chunk: %q", text)
	}
}

// TestACPExtensionCommandInsertDegradesToChunk proves verification (d): the
// TUI-only insert action degrades to a display-style agent_message_chunk (no
// editor input surface to fill), resolves end_turn, and never calls the model
// or crashes.
func TestACPExtensionCommandInsertDegradesToChunk(t *testing.T) {
	client := &countingTextClient{}
	h, sid, _, teardown := extCommandSetup(t, client)
	defer teardown()

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/fillin draft"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/fillin stopReason = %q; want end_turn", res["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for an insert command; want 0", got)
	}
	text := drainChunkText(h.drainUpdates())
	if !strings.Contains(text, "prefilled:draft") {
		t.Errorf("insert action did not degrade to a chunk carrying the would-be insert text: %q", text)
	}
}

// TestACPExtensionCommandOpenPanelDegradesToChunk proves the open_panel
// degradation (verification (d) sibling): the TUI-only panel renders its
// content as a chunk, no crash, no model call.
func TestACPExtensionCommandOpenPanelDegradesToChunk(t *testing.T) {
	client := &countingTextClient{}
	h, sid, _, teardown := extCommandSetup(t, client)
	defer teardown()

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/showpanel"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/showpanel stopReason = %q; want end_turn", res["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for an open_panel command; want 0", got)
	}
	text := drainChunkText(h.drainUpdates())
	for _, want := range []string{"Panel Title", "line one", "line two", "the footer"} {
		if !strings.Contains(text, want) {
			t.Errorf("open_panel degradation chunk missing %q; got: %q", want, text)
		}
	}
}

// TestACPExtensionCommandErrorAction proves the error action surfaces as a
// chunk and ends the turn without calling the model.
func TestACPExtensionCommandErrorAction(t *testing.T) {
	client := &countingTextClient{}
	h, sid, _, teardown := extCommandSetup(t, client)
	defer teardown()

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/boom"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/boom stopReason = %q; want end_turn", res["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for an error command; want 0", got)
	}
	text := drainChunkText(h.drainUpdates())
	if !strings.Contains(text, "kaboom") {
		t.Errorf("error action did not surface the extension Error text as a chunk: %q", text)
	}
}

// TestACPExtensionCommandCollisionBuiltinWins proves verification (e): a name
// the extension also registers (`help`) is handled by the built-in /help, NOT
// the extension — the built-in's terva-overview chunk appears and the
// extension's sentinel ("EXT-HELP-SHOULD-NOT-APPEAR") never does.
func TestACPExtensionCommandCollisionBuiltinWins(t *testing.T) {
	client := &countingTextClient{}
	h, sid, _, teardown := extCommandSetup(t, client)
	defer teardown()

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/help"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/help stopReason = %q; want end_turn", res["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for /help; want 0 (built-in native execution)", got)
	}
	text := drainChunkText(h.drainUpdates())
	if strings.Contains(text, "EXT-HELP-SHOULD-NOT-APPEAR") {
		t.Error("the extension's help handler ran for /help; the built-in must win the collision")
	}
	if !strings.Contains(text, "terva") {
		t.Errorf("/help did not run the built-in overview: %q", text)
	}
}

// TestACPExtensionContextAndReloadAdvertised proves verification (c) for the
// two extMgr-reading built-ins: /context and /reload-ext are both advertised in
// session/new's catalog (alongside the rest of the curated set), so the editor's
// command palette offers them.
func TestACPExtensionContextAndReloadAdvertised(t *testing.T) {
	_, _, catalog, teardown := extCommandSetup(t, &countingTextClient{})
	defer teardown()

	names, found := findAvailableCommands([]map[string]any{catalog})
	if !found {
		t.Fatal("session/new did not emit an available_commands_update")
	}
	for _, want := range []string{"context", "reload-ext"} {
		if !names[want] {
			t.Errorf("available_commands_update missing built-in /%s", want)
		}
	}
}

// TestACPSlashContextShowsExtensionContext proves verification (a): /context
// emits a chunk naming each contributing extension plus its injected text
// (static guidance + the live card), without calling the model, and resolves
// end_turn.
func TestACPSlashContextShowsExtensionContext(t *testing.T) {
	client := &countingTextClient{}
	h, sid, _, teardown := extCommandSetup(t, client)
	defer teardown()

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/context"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/context stopReason = %q; want end_turn", res["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for /context; want 0 (read-only, no model call)", got)
	}
	text := drainChunkText(h.drainUpdates())
	// Names the contributing extension (webext) and surfaces both the static
	// guidance and the live card's text.
	for _, want := range []string{
		"webext (system guidance):",
		"always prefer the webext API for fetches",
		`webext (card "open tasks"):`,
		"task one",
		"task two",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("/context chunk missing %q; got: %q", want, text)
		}
	}
}

// TestACPSlashContextNoContextDegrades proves verification (a) graceful path:
// with no extension wired (extContext nil), /context degrades to the "extensions
// are not enabled" note — a chunk, end_turn, no model call, no panic.
func TestACPSlashContextNoContextDegrades(t *testing.T) {
	client := &countingTextClient{}
	factory := &fakeFactory{client: client, tools: core.Registry{}} // no extRoot -> nil extContext
	h, sid, teardown := commandSetup(t, factory)
	defer teardown()
	_ = h.drainUpdates()

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/context"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/context (no ext) stopReason = %q; want end_turn", res["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for /context with no extensions; want 0", got)
	}
	if text := drainChunkText(h.drainUpdates()); !strings.Contains(text, "not enabled") {
		t.Errorf("/context with no extensions did not degrade gracefully: %q", text)
	}
}

// TestACPSlashContextEmptyContextDegrades proves the "manager wired but nothing
// injected" path: an extContext closure that returns no items yields the "no
// extension is contributing context" note (distinct from the "not enabled" note
// when the closure is nil). Driven through the full wire with a present-but-
// empty ExtContext on the SessionAgent.
func TestACPSlashContextEmptyContextDegrades(t *testing.T) {
	client := &countingTextClient{}
	factory := &fakeFactory{client: client, tools: core.Registry{}, emptyExtContext: true}
	h, sid, teardown := commandSetup(t, factory)
	defer teardown()
	_ = h.drainUpdates()

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/context"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/context (empty) stopReason = %q; want end_turn", res["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for /context with empty context; want 0", got)
	}
	text := drainChunkText(h.drainUpdates())
	if !strings.Contains(text, "No extension is contributing context") {
		t.Errorf("/context with an empty snapshot did not report the no-context note: %q", text)
	}
	if strings.Contains(text, "not enabled") {
		t.Errorf("/context with a wired-but-empty manager wrongly reported 'not enabled': %q", text)
	}
}

// TestACPSlashReloadExtReloadsAndReadvertises proves verification (b):
// /reload-ext invokes the host reload closure (the counter increments), emits
// the stats chunk, AND re-emits available_commands_update — all without calling
// the model, resolving end_turn.
func TestACPSlashReloadExtReloadsAndReadvertises(t *testing.T) {
	root := installFakeExtension(t)
	client := &countingTextClient{}
	factory := &fakeFactory{client: client, tools: core.Registry{}, extRoot: root}
	h, drain, teardown := serveExt(t, factory)
	defer teardown()

	newRes := h.call(MethodSessionNew, map[string]any{"cwd": t.TempDir()})
	sid, _ := newRes["sessionId"].(string)
	if sid == "" {
		t.Fatal("session/new returned empty sessionId")
	}
	drain() // the post-session/new catalog

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/reload-ext"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/reload-ext stopReason = %q; want end_turn", res["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for /reload-ext; want 0", got)
	}
	// The reload closure actually ran.
	if got := atomic.LoadInt32(&factory.extReloads); got != 1 {
		t.Errorf("ReloadExtensions ran %d times; want 1 (the command must invoke the reload closure)", got)
	}

	updates := h.drainUpdates()
	// The stats chunk surfaced (the extstub re-registers two commands on
	// respawn, so "loaded" is non-zero — but the wording, not the exact count,
	// is what we assert).
	if text := drainChunkText(updates); !strings.Contains(text, "reloaded:") {
		t.Errorf("/reload-ext did not emit a stats chunk: %q", text)
	}
	// The ACP-specific bit: the command catalog is re-advertised so the editor
	// learns any changed extension command set.
	names, found := findAvailableCommands(updates)
	if !found {
		t.Error("/reload-ext did not re-emit an available_commands_update")
	}
	// The reloaded command set is still advertised (the extstub re-registers it).
	if !names["showinfo"] {
		t.Errorf("re-advertised catalog missing the reloaded extension command /showinfo: %v", names)
	}
}

// TestACPSlashReloadExtNoManagerDegrades proves the graceful path (verification
// (d)): with no extension manager (reloadExtensions nil), /reload-ext degrades
// to a note, end_turn, no model call, no panic.
func TestACPSlashReloadExtNoManagerDegrades(t *testing.T) {
	client := &countingTextClient{}
	factory := &fakeFactory{client: client, tools: core.Registry{}} // no extRoot -> nil reloadExtensions
	h, sid, teardown := commandSetup(t, factory)
	defer teardown()
	_ = h.drainUpdates()

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/reload-ext"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/reload-ext (no manager) stopReason = %q; want end_turn", res["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for /reload-ext with no manager; want 0", got)
	}
	if text := drainChunkText(h.drainUpdates()); !strings.Contains(text, "No extension manager") {
		t.Errorf("/reload-ext with no manager did not degrade gracefully: %q", text)
	}
}

// TestACPUnknownSlashStillDegrades proves an unknown slash command (neither
// built-in nor extension) keeps the graceful-degradation note even with
// extensions wired — it is not routed to the extension layer or the model.
func TestACPUnknownSlashStillDegrades(t *testing.T) {
	client := &countingTextClient{}
	h, sid, _, teardown := extCommandSetup(t, client)
	defer teardown()

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/definitelynotacommand"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("unknown slash stopReason = %q; want end_turn", res["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for an unknown slash; want 0", got)
	}
	text := drainChunkText(h.drainUpdates())
	if !strings.Contains(text, "Unknown command") {
		t.Errorf("unknown slash did not degrade to a note: %q", text)
	}
}
