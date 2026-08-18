//go:build terva_acp

package acp

// A session/load that rebinds an id while a turn is running on it.
//
// conn.run dispatches every inbound request on its own goroutine, so a
// session/load genuinely runs concurrently with an in-flight session/prompt —
// nothing serialises them. bindSession used to take only agentServer.mu: it
// swapped the map entry, called prev.cleanup() (killing the MCP and extension
// subprocesses the running tools were using) and prev.durable.Close() (closing
// the file the agent's message observer was still appending to). The observer's
// error is swallowed by build.WireHeadlessSessionPersist, so the turn's rows
// vanished from the transcript with nothing logged anywhere.
//
// The old test only rebound an IDLE session, which is why this held for so long.

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// parkedTurnClient streams a turn that stops partway and waits, so a test can
// hold a turn open across another request.
//
// It parks BEFORE emitting EventDone, because EventDone is what carries the
// assistant message that gets appended to the transcript. Parking after it
// would let the row land before the rebind and prove nothing.
type parkedTurnClient struct {
	reply     string
	started   chan struct{} // closed once the turn is genuinely in flight
	release   chan struct{} // close to let a NON-cancelled turn finish
	startOnce sync.Once
}

func newParkedTurnClient(reply string) *parkedTurnClient {
	return &parkedTurnClient{
		reply:   reply,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (c *parkedTurnClient) Name() string { return "fake-acp-parked" }

func (c *parkedTurnClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 8)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: c.Name(), Model: req.Model}
		out <- provider.EventTextDelta{Delta: "thinking"}
		c.startOnce.Do(func() { close(c.started) })
		// Park. A cancelled turn takes the ctx branch and still emits its
		// message, which is the behaviour under test: a turn told to stop
		// finishes writing what it had.
		select {
		case <-c.release:
		case <-ctx.Done():
		}
		out <- provider.EventDone{
			Stop: provider.StopEnd,
			Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.TextBlock{Text: c.reply}},
			},
		}
	}()
	return out, nil
}

// awaitFrame returns the raw response frame for reqID, error included.
//
// harness.awaitResponse t.Fatals on an error response, which is the right
// default everywhere else and useless here: the error IS the assertion.
func awaitFrame(t *testing.T, h *harness, reqID int) frame {
	t.Helper()
	if f, ok := h.pending[reqID]; ok {
		delete(h.pending, reqID)
		return f
	}
	for {
		f, err := h.read1()
		if err != nil {
			t.Fatalf("awaiting response to request %d: %v", reqID, err)
		}
		if f.method != "" {
			continue
		}
		if rid, ok := f.id.(float64); ok && int(rid) == reqID {
			return f
		}
		h.keep(f)
	}
}

// The headline: rebinding mid-turn must not cost the turn's transcript.
func TestACPRebindMidTurnKeepsTheTurnsTranscript(t *testing.T) {
	const reply = "the row that must survive the rebind"
	root := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	client := newParkedTurnClient(reply)
	factory := &fakeFactory{client: client, tools: core.Registry{}, root: root}

	h, drain, teardown := serveExt(t, factory)
	defer teardown()

	sid, _ := h.call(MethodSessionNew, map[string]any{"cwd": cwd})["sessionId"].(string)
	if sid == "" {
		t.Fatal("session/new returned empty sessionId")
	}
	drain()

	promptID := h.send(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []any{map[string]any{"type": "text", "text": "go"}},
	})

	// Wait for the turn to be genuinely in flight before rebinding — without
	// this the test races and can rebind an idle session, which is the case
	// that already worked.
	select {
	case <-client.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the parked turn never started; the test is not exercising a live turn")
	}

	loadID := h.send(MethodSessionLoad, map[string]any{
		"sessionId":  sid,
		"cwd":        cwd,
		"mcpServers": []any{},
	})
	_ = h.awaitResponse(loadID, nil)

	// Let a turn that was NOT cancelled finish, so a regression fails the
	// assertion below instead of hanging the test.
	close(client.release)
	_ = h.awaitResponse(promptID, nil)

	// The sessionId IS the durable path, so the transcript can be read back
	// directly. Give the close/flush a moment either way.
	deadline := time.Now().Add(3 * time.Second)
	var body string
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(sid)
		if err == nil {
			body = string(b)
			if strings.Contains(body, reply) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(body, reply) {
		t.Errorf("the turn's assistant message is missing from the transcript after a mid-turn rebind.\n"+
			"bindSession closed the durable session while the agent's observer was still appending to it, "+
			"and WireHeadlessSessionPersist swallows that error — so the row is gone with nothing logged.\n"+
			"transcript:\n%s", body)
	}
}

// The other half: a prompt that was QUEUED behind the cancelled turn must not
// then run on the retired binding.
//
// It would find the old session still in the map, block on its turnMu behind
// the turn being cancelled, and start once that released — against subprocesses
// cleanup() has since killed and a durable session that is closed. The caller
// is told to send it again rather than served a turn whose output goes nowhere.
func TestACPAPromptQueuedBehindARebindIsRefusedNotOrphaned(t *testing.T) {
	root := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	client := newParkedTurnClient("first")
	factory := &fakeFactory{client: client, tools: core.Registry{}, root: root}

	h, drain, teardown := serveExt(t, factory)
	defer teardown()

	sid, _ := h.call(MethodSessionNew, map[string]any{"cwd": cwd})["sessionId"].(string)
	if sid == "" {
		t.Fatal("session/new returned empty sessionId")
	}
	drain()

	first := h.send(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []any{map[string]any{"type": "text", "text": "one"}},
	})
	select {
	case <-client.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the parked turn never started")
	}

	// A second prompt for the same id: it blocks on the OLD session's turnMu.
	second := h.send(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []any{map[string]any{"type": "text", "text": "two"}},
	})

	loadID := h.send(MethodSessionLoad, map[string]any{
		"sessionId":  sid,
		"cwd":        cwd,
		"mcpServers": []any{},
	})
	_ = h.awaitResponse(loadID, nil)
	close(client.release)
	_ = h.awaitResponse(first, nil)

	// The queued prompt must come back as an error naming the reload, not as a
	// silently-successful turn against a torn-down session.
	f := awaitFrame(t, h, second)
	if f.errObj == nil {
		t.Fatalf("the queued prompt returned a normal result after its binding was retired; "+
			"it ran against a closed durable session and killed subprocesses. result=%v", f.result)
	}
	if msg, _ := f.errObj["message"].(string); !strings.Contains(msg, "reloaded") {
		t.Errorf("the refusal does not tell the caller what happened: %q", msg)
	}
}

// A turn that ignores cancellation must not make the rebind silent.
//
// The interlock cannot force a wedged turn to stop, so the supersede proceeds
// after the grace period — which means the tail of that turn's transcript CAN
// still be lost. The one thing that must not happen is losing it quietly: this
// finding exists because the loss had no signal anywhere.
func TestACPARebindThatCannotWaitSaysSo(t *testing.T) {
	prev := rebindGrace
	rebindGrace = 150 * time.Millisecond // exercise the branch, not the clock
	defer func() { rebindGrace = prev }()

	root := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	client := newWedgedTurnClient()
	defer client.stop()
	factory := &fakeFactory{client: client, tools: core.Registry{}, root: root}

	h, drain, teardown := serveExt(t, factory)
	defer teardown()

	sid, _ := h.call(MethodSessionNew, map[string]any{"cwd": cwd})["sessionId"].(string)
	if sid == "" {
		t.Fatal("session/new returned empty sessionId")
	}
	drain()

	h.send(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []any{map[string]any{"type": "text", "text": "go"}},
	})
	select {
	case <-client.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the wedged turn never started")
	}

	loadID := h.send(MethodSessionLoad, map[string]any{
		"sessionId":  sid,
		"cwd":        cwd,
		"mcpServers": []any{},
	})
	_ = h.awaitResponse(loadID, nil)

	// The warning rides the session/update stream, which the harness buffers.
	// The payload is nested under "update" — session/update params are
	// {sessionId, update:{sessionUpdate, content}}, not the payload flattened.
	var said bool
	for _, u := range h.drainUpdates() {
		up, _ := u["update"].(map[string]any)
		c, _ := up["content"].(map[string]any)
		if txt, _ := c["text"].(string); strings.Contains(txt, "may be missing") {
			said = true
		}
	}
	if !said {
		t.Error("a rebind that timed out waiting for a wedged turn said nothing; the dropped " +
			"transcript tail is exactly the silence this interlock exists to end")
	}
}

// wedgedTurnClient never finishes and ignores cancellation — the turn the
// interlock cannot rescue.
type wedgedTurnClient struct {
	started   chan struct{}
	quit      chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
}

func newWedgedTurnClient() *wedgedTurnClient {
	return &wedgedTurnClient{started: make(chan struct{}), quit: make(chan struct{})}
}

func (c *wedgedTurnClient) stop() { c.stopOnce.Do(func() { close(c.quit) }) }

func (c *wedgedTurnClient) Name() string { return "fake-acp-wedged" }

func (c *wedgedTurnClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 8)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: c.Name(), Model: req.Model}
		c.startOnce.Do(func() { close(c.started) })
		<-c.quit // deliberately NOT selecting on ctx.Done
	}()
	return out, nil
}

// rebindGrace is a var so the timeout test above can exercise that branch
// without spending the real grace period. That is only acceptable if the
// production value is itself checked — otherwise "shorten it to 1ms" becomes a
// way to delete the wait with every test still green.
//
// The floor is about what the wait is FOR: a cancelled turn finishing a write.
// Sub-second would routinely time out on an ordinary flush and turn the warning
// into noise; unbounded-large would let a wedged turn hang session/load.
func TestRebindGraceIsGenerousEnoughToFlush(t *testing.T) {
	if rebindGrace < 5*time.Second {
		t.Errorf("rebindGrace is %s: too short for a cancelled turn to flush its transcript, "+
			"so the timeout warning would fire on healthy rebinds", rebindGrace)
	}
	if rebindGrace > 30*time.Second {
		t.Errorf("rebindGrace is %s: long enough that a wedged turn hangs session/load "+
			"and the editor looks frozen", rebindGrace)
	}
}
