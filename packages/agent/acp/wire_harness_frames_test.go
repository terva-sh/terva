//go:build terva_acp

package acp

import (
	"io"
	"strings"
	"testing"
	"time"
)

// These cover the test harness itself rather than the agent. Both properties
// were violated at once, and together they cost a CI job 777 seconds: a
// response was silently dropped, and the waiter that then blocked forever had
// a deadline it could never reach, so the package ran until Go's 10-minute
// hang panic instead of failing in milliseconds.

// TestHarnessKeepsOutOfOrderResponses: awaiting one request must not discard
// another's response. Two prompts in flight serialize on the session turn
// mutex, but each writes its response after releasing it — so on a contended
// runner the second can reach the wire first. JSON-RPC permits exactly that
// for concurrent requests, and the harness has to cope.
func TestHarnessKeepsOutOfOrderResponses(t *testing.T) {
	// Request 2 answered before request 1.
	wire := `{"jsonrpc":"2.0","id":2,"result":{"which":"second"}}
{"jsonrpc":"2.0","id":1,"result":{"which":"first"}}
`
	h := newHarness(t, io.Discard, strings.NewReader(wire))
	id1 := h.send(MethodSessionPromptName, map[string]any{"n": 1})
	id2 := h.send(MethodSessionPromptName, map[string]any{"n": 2})
	if id1 != 1 || id2 != 2 {
		t.Fatalf("request ids = %d, %d; want 1, 2", id1, id2)
	}

	// Scanning for request 1 sees request 2's response first. If that frame is
	// dropped, this next line is the last one that ever runs.
	if got := h.awaitResponse(id1, nil)["which"]; got != "first" {
		t.Errorf("response to %d = %v; want %q", id1, got, "first")
	}
	if got := h.awaitResponse(id2, nil)["which"]; got != "second" {
		t.Errorf("response to %d = %v; want %q", id2, got, "second")
	}
}

// TestHarnessKeepsResponseOvertakingPermission: same loss, different waiter —
// awaitPermission is also a scanning loop, so a response that arrives while
// the agent is still working up to its permission request must survive.
func TestHarnessKeepsResponseOvertakingPermission(t *testing.T) {
	wire := `{"jsonrpc":"2.0","id":1,"result":{"which":"first"}}
{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"toolCall":{"toolCallId":"tc-1"}}}
`
	h := newHarness(t, io.Discard, strings.NewReader(wire))
	id1 := h.send(MethodSessionPromptName, map[string]any{"n": 1})

	permID, tcid := h.awaitPermission()
	if tcid != "tc-1" {
		t.Errorf("toolCallId = %q; want %q", tcid, "tc-1")
	}
	if rid, _ := permID.(float64); int(rid) != 7 {
		t.Errorf("permission id = %v; want 7", permID)
	}
	if got := h.awaitResponse(id1, nil)["which"]; got != "first" {
		t.Errorf("response to %d = %v; want %q", id1, got, "first")
	}
}

// TestHarnessReadDeadlineIsEnforced: a silent peer must surface as an error,
// not a hang. The previous helpers checked a deadline at the top of a loop
// whose body then blocked in json.Decoder.Decode — which has no read deadline
// over an io.Pipe — so once parked they never returned to the check.
func TestHarnessReadDeadlineIsEnforced(t *testing.T) {
	// A pipe nobody writes to and nobody closes: the shape of a deadlocked
	// peer. It never yields a frame and never reaches EOF.
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	h := newHarness(t, io.Discard, pr)

	start := time.Now()
	if _, err := h.nextFrame(100 * time.Millisecond); err == nil {
		t.Fatal("nextFrame succeeded on a silent stream; want a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("nextFrame took %s to give up; the deadline is not being enforced", elapsed)
	}
}

// TestHarnessReportsStreamEnd: the other way a wait can end. An agent that
// dies mid-turn should name that, rather than time out 30s later.
func TestHarnessReportsStreamEnd(t *testing.T) {
	h := newHarness(t, io.Discard, strings.NewReader(""))
	if _, err := h.nextFrame(harnessTimeout); err == nil {
		t.Fatal("nextFrame succeeded on a closed stream; want an error")
	} else if !strings.Contains(err.Error(), "closed") {
		t.Errorf("error = %v; want it to name the closed stream", err)
	}
}
