package core

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

type recordingConfirmer struct {
	mu      sync.Mutex
	calls   []string
	replies []ConfirmDecision
	idx     int
}

func (r *recordingConfirmer) Confirm(_ context.Context, toolName, preview string) ConfirmDecision {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, toolName+"/"+preview)
	if r.idx >= len(r.replies) {
		return ConfirmDecision{Allow: false, Reason: "no reply queued"}
	}
	d := r.replies[r.idx]
	r.idx++
	return d
}

func TestConfirmGateNilAllowsEverything(t *testing.T) {
	var g *ConfirmGate
	allow, reason, args := g.Check(context.Background(), "bash", nil, "rm -rf /", "")
	if !allow || reason != "" || args != nil {
		t.Fatalf("nil gate should allow, got allow=%v reason=%q args=%s", allow, reason, args)
	}
}

func TestConfirmGateNilInnerRefuses(t *testing.T) {
	g := NewConfirmGate(nil)
	allow, reason, _ := g.Check(context.Background(), "bash", nil, "ls", "")
	if allow {
		t.Fatal("gate with nil inner should refuse")
	}
	if reason == "" {
		t.Fatal("refusal must carry a reason for the model to learn from")
	}
}

func TestConfirmGateAllowOnce(t *testing.T) {
	rc := &recordingConfirmer{replies: []ConfirmDecision{
		{Allow: true},
		{Allow: true},
	}}
	g := NewConfirmGate(rc)
	if allow, _, _ := g.Check(context.Background(), "bash", nil, "ls", ""); !allow {
		t.Fatal("call 1 should allow")
	}
	if allow, _, _ := g.Check(context.Background(), "bash", nil, "ls", ""); !allow {
		t.Fatal("call 2 should allow")
	}
	// Two calls, two confirmer invocations (no remember).
	if len(rc.calls) != 2 {
		t.Errorf("want 2 confirmer calls, got %d", len(rc.calls))
	}
}

func TestConfirmGateRememberTool(t *testing.T) {
	rc := &recordingConfirmer{replies: []ConfirmDecision{
		{Allow: true, RememberTool: true},
	}}
	g := NewConfirmGate(rc)

	// First call prompts and remembers.
	if allow, _, _ := g.Check(context.Background(), "bash", nil, "ls", ""); !allow {
		t.Fatal("call 1 should allow")
	}
	// Second call short-circuits; confirmer must NOT be invoked.
	if allow, _, _ := g.Check(context.Background(), "bash", nil, "pwd", ""); !allow {
		t.Fatal("call 2 should allow from memory")
	}
	// Different tool still prompts.
	rc.replies = append(rc.replies, ConfirmDecision{Allow: false, Reason: "no"})
	if allow, reason, _ := g.Check(context.Background(), "read", nil, "foo.txt", ""); allow || reason != "no" {
		t.Errorf("different tool should re-prompt; got allow=%v reason=%q", allow, reason)
	}
	if len(rc.calls) != 2 {
		t.Errorf("want 2 confirmer calls (bash+read), got %d: %v", len(rc.calls), rc.calls)
	}
}

func TestConfirmGateRememberAll(t *testing.T) {
	rc := &recordingConfirmer{replies: []ConfirmDecision{
		{Allow: true, RememberAll: true},
	}}
	g := NewConfirmGate(rc)
	if allow, _, _ := g.Check(context.Background(), "bash", nil, "ls", ""); !allow {
		t.Fatal("call 1 should allow")
	}
	// From now on everything short-circuits.
	if allow, _, _ := g.Check(context.Background(), "read", nil, "foo.txt", ""); !allow {
		t.Fatal("call 2 should allow")
	}
	if allow, _, _ := g.Check(context.Background(), "write", nil, "bar.txt", ""); !allow {
		t.Fatal("call 3 should allow")
	}
	if len(rc.calls) != 1 {
		t.Errorf("want 1 confirmer call (remember-all short-circuits the rest), got %d", len(rc.calls))
	}
}

func TestConfirmGateRefuseSurfacesReason(t *testing.T) {
	rc := &recordingConfirmer{replies: []ConfirmDecision{
		{Allow: false, Reason: "do not run sudo"},
	}}
	g := NewConfirmGate(rc)
	allow, reason, _ := g.Check(context.Background(), "bash", nil, "sudo rm -rf /", "")
	if allow || reason != "do not run sudo" {
		t.Errorf("want block + reason, got allow=%v reason=%q", allow, reason)
	}
}

func TestConfirmGateRefuseEmptyReasonGetsDefault(t *testing.T) {
	rc := &recordingConfirmer{replies: []ConfirmDecision{
		{Allow: false},
	}}
	g := NewConfirmGate(rc)
	_, reason, _ := g.Check(context.Background(), "bash", nil, "x", "")
	if reason == "" {
		t.Fatal("empty reason must be replaced with a sensible default")
	}
}

func TestConfirmGateAllowAllRuntime(t *testing.T) {
	rc := &recordingConfirmer{replies: []ConfirmDecision{
		{Allow: false, Reason: "no"},
	}}
	g := NewConfirmGate(rc)
	// Refuse first call
	if allow, _, _ := g.Check(context.Background(), "bash", nil, "ls", ""); allow {
		t.Fatal("call 1 should refuse")
	}
	// User types /yolo
	g.AllowAll()
	// All subsequent calls allowed without prompting.
	if allow, _, _ := g.Check(context.Background(), "bash", nil, "rm -rf tmp", ""); !allow {
		t.Fatal("after AllowAll, should allow")
	}
	if allow, _, _ := g.Check(context.Background(), "read", nil, "x", ""); !allow {
		t.Fatal("after AllowAll, should allow any tool")
	}
	if len(rc.calls) != 1 {
		t.Errorf("want 1 confirmer call (before AllowAll), got %d", len(rc.calls))
	}
}

func TestConfirmGateRevokeReprompts(t *testing.T) {
	rc := &recordingConfirmer{replies: []ConfirmDecision{
		{Allow: true, RememberTool: true}, // grant bash for the session
		{Allow: true},                     // re-prompt after revoke
	}}
	g := NewConfirmGate(rc)

	if allow, _, _ := g.Check(context.Background(), "bash", nil, "ls", ""); !allow {
		t.Fatal("call 1 should allow + remember")
	}
	// Grant is visible in the inspector snapshot.
	if all, tools := g.Grants(); all || len(tools) != 1 || tools[0] != "bash" {
		t.Fatalf("Grants() = (%v, %v), want (false, [bash])", all, tools)
	}
	// Revoke it: the next call must prompt again.
	g.Revoke("bash")
	if all, tools := g.Grants(); all || len(tools) != 0 {
		t.Fatalf("after Revoke, Grants() = (%v, %v), want (false, [])", all, tools)
	}
	if allow, _, _ := g.Check(context.Background(), "bash", nil, "pwd", ""); !allow {
		t.Fatal("call 2 should allow (via the second queued reply)")
	}
	if len(rc.calls) != 2 {
		t.Errorf("want 2 confirmer calls (revoke forced a re-prompt), got %d", len(rc.calls))
	}
	// Revoking an unknown tool is a harmless no-op.
	g.Revoke("never-granted")
}

func TestConfirmGateClearAllowAllKeepsToolGrants(t *testing.T) {
	rc := &recordingConfirmer{replies: []ConfirmDecision{
		{Allow: true, RememberTool: true}, // grant edit
		{Allow: true, RememberAll: true},  // then allow-all
	}}
	g := NewConfirmGate(rc)
	if allow, _, _ := g.Check(context.Background(), "edit", nil, "f", ""); !allow {
		t.Fatal("call 1 should allow + remember edit")
	}
	if allow, _, _ := g.Check(context.Background(), "bash", nil, "ls", ""); !allow {
		t.Fatal("call 2 should allow + remember-all")
	}
	if all, _ := g.Grants(); !all {
		t.Fatal("expected allowAll after RememberAll")
	}
	// Dropping the blanket grant must leave the per-tool grant intact.
	g.ClearAllowAll()
	all, tools := g.Grants()
	if all {
		t.Fatal("ClearAllowAll should clear the blanket grant")
	}
	if len(tools) != 1 || tools[0] != "edit" {
		t.Fatalf("per-tool grants should survive, got %v", tools)
	}
	// edit still short-circuits; a fresh tool prompts again.
	if allow, _, _ := g.Check(context.Background(), "edit", nil, "g", ""); !allow {
		t.Fatal("edit should still allow from its surviving grant")
	}
	rc.replies = append(rc.replies, ConfirmDecision{Allow: false, Reason: "no"})
	if allow, _, _ := g.Check(context.Background(), "read", nil, "x", ""); allow {
		t.Fatal("read should prompt again now that allowAll is cleared")
	}
}

func TestBuildPreview(t *testing.T) {
	cases := []struct {
		name string
		args string
		want string
	}{
		{"bash command", `{"command":"ls -la"}`, "ls -la"},
		{"path", `{"path":"/tmp/x.txt"}`, "/tmp/x.txt"},
		{"file_path", `{"file_path":"a.go"}`, "a.go"},
		{"url", `{"url":"https://example.com"}`, "https://example.com"},
		{"truncation", `{"command":"` + string(make([]byte, 200)) + `"}`, ""},
		{"unparseable", `{not json`, `{not json`},
		{"empty", ``, ``},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := BuildPreview(json.RawMessage(c.args), 50)
			if c.name == "truncation" && !hasEllipsis(got) {
				t.Errorf("%s: expected ellipsis truncation, got %q", c.name, got)
				return
			}
			if c.want != "" && got != c.want {
				t.Errorf("%s: want %q, got %q", c.name, c.want, got)
			}
		})
	}
}

func hasEllipsis(s string) bool {
	return strings.HasSuffix(s, "...")
}

// A call whose turn is already over must not reach a human at all.
//
// Every confirmer selects on the context and would deny on its own, so the
// verdict is the same either way — but only refusing HERE keeps the question off
// somebody's screen. An approval prompt for a cancelled turn is worse than
// useless: it spends attention, and whatever the person answers, the call cannot
// run (runOneTool re-checks after the ladder). The gate has the context, so the
// gate is where the ask stops.
func TestAnAlreadyCancelledCallIsRefusedWithoutAsking(t *testing.T) {
	rc := &recordingConfirmer{replies: []ConfirmDecision{{Allow: true}}}
	g := NewConfirmGate(rc)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	allow, reason, _ := g.Check(ctx, "bash", nil, "rm -rf /", "call-1")
	if allow {
		t.Error("a cancelled call must be refused even though the confirmer would have allowed it")
	}
	if !strings.Contains(reason, "cancelled") {
		t.Errorf("reason = %q, want it to name the cancellation so the model can tell it apart from a refusal", reason)
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if len(rc.calls) != 0 {
		t.Errorf("the confirmer was asked %v about a turn that no longer exists", rc.calls)
	}
}

// The order matters: a DENY rule still beats a cancelled context, because the
// reason the model sees should name the rule that would always have refused
// rather than a cancellation that merely happened to arrive first. And an ALLOW
// rule never reaches the ask at all, so cancelling changes nothing for it.
func TestPolicyStillDecidesBeforeTheCancellationCheck(t *testing.T) {
	rc := &recordingConfirmer{}
	g := NewPolicyGate(&PermissionPolicy{
		Mode:  ApprovalAsk,
		Rules: []PermissionRule{{Tool: "bash", Decision: RuleDeny, Reason: "no shell here"}},
	}, rc)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if allow, reason, _ := g.Check(ctx, "bash", nil, "ls", ""); allow || !strings.Contains(reason, "no shell here") {
		t.Errorf("allow=%v reason=%q, want the deny rule's own reason", allow, reason)
	}
}
