package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The refusal this replaces told the MODEL to "ask the user to trust it". In
// a 17.5-hour session that hit it five times over 24 hours, the model relayed
// it zero times — not one of its 195 user-facing messages mentioned trust. A
// block only a human can clear has to be delivered to the human, so it goes
// to the approval gate instead.
func TestUntrustedSpawnAsksTheUser(t *testing.T) {
	tool, sw := newSpawnToolForTrust(t, false)
	asked := 0
	var gotPreview string
	tool.ConfirmUntrusted = func(_ context.Context, preview string) (bool, string) {
		asked++
		gotPreview = preview
		return false, "user declined"
	}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"review the repo"}`), nil)
	if err != nil {
		t.Fatalf("Execute returned hard error %v; want a soft tool error", err)
	}
	if asked != 1 {
		t.Fatalf("gate consulted %d times, want 1", asked)
	}
	// The prompt carries the whole decision: the dialog shows a tool name and
	// this line, and "swarm_spawn_untrusted" alone says nothing about cost.
	for _, want := range []string{"untrusted", "WITHOUT", "degraded", "/trust"} {
		if !strings.Contains(gotPreview, want) {
			t.Errorf("preview missing %q: %q", want, gotPreview)
		}
	}
	if !res.IsError {
		t.Fatal("a declined spawn did not soft-fail")
	}
	body := spawnToolText(res.Content[0])
	if !strings.Contains(body, "declined") {
		t.Fatalf("refusal does not say the user declined: %q", body)
	}
	// The model must stop hammering the same wall — that is the whole defect.
	if !strings.Contains(body, "Do NOT retry") {
		t.Fatalf("refusal does not tell the model to stop retrying: %q", body)
	}
	if got := len(sw.List()); got != 0 {
		t.Fatalf("agents spawned despite refusal: %d", got)
	}
}

// Allowing means "run them degraded": the spawn actually happens.
func TestUntrustedSpawnProceedsWhenAllowed(t *testing.T) {
	tool, sw := newSpawnToolForTrust(t, false)
	tool.ConfirmUntrusted = func(_ context.Context, _ string) (bool, string) { return true, "" }

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"review the repo"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("an allowed spawn still failed: %q", spawnToolText(res.Content[0]))
	}
	if got := len(sw.List()); got != 1 {
		t.Fatalf("spawned %d agents, want 1", got)
	}
}

// A trusted workspace must never reach the gate. Prompting for something the
// user already settled is how a prompt teaches people to dismiss prompts.
func TestTrustedWorkspaceNeverAsks(t *testing.T) {
	tool, _ := newSpawnToolForTrust(t, true)
	asked := 0
	tool.ConfirmUntrusted = func(_ context.Context, _ string) (bool, string) { asked++; return true, "" }

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"x"}`), nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if asked != 0 {
		t.Fatalf("gate consulted %d times for a trusted workspace, want 0", asked)
	}
}

// allow_untrusted stays the explicit override: the user already said yes
// through the argument, so asking again is noise.
func TestAllowUntrustedSkipsTheGate(t *testing.T) {
	tool, _ := newSpawnToolForTrust(t, false)
	asked := 0
	tool.ConfirmUntrusted = func(_ context.Context, _ string) (bool, string) { asked++; return true, "" }

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"x","allow_untrusted":true}`), nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if asked != 0 {
		t.Fatalf("gate consulted %d times with allow_untrusted, want 0", asked)
	}
}

// No gate wired — headless, rpc, pure yolo, a sub-agent's own registry. There
// is nobody to ask, so the old refusal-with-guidance stands rather than
// blocking a turn on a prompt that can never be answered.
//
// (TestSwarmSpawnRefusesUntrustedWorkspace covers the same path from the
// other side: it never sets ConfirmUntrusted and still passes unchanged.)
func TestUntrustedSpawnWithoutAGateKeepsTheOldRefusal(t *testing.T) {
	tool, _ := newSpawnToolForTrust(t, false)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"x"}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatal("headless untrusted spawn did not soft-fail")
	}
	body := spawnToolText(res.Content[0])
	for _, want := range []string{"untrusted", "/trust", "allow_untrusted"} {
		if !strings.Contains(body, want) {
			t.Errorf("headless refusal lost %q: %q", want, body)
		}
	}
	if strings.Contains(body, "declined") {
		t.Fatalf("headless refusal reads as a user decision: %q", body)
	}
}
