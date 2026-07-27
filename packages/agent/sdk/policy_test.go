package sdk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// The SDK used to build an agent with no ConfirmGate at all: every tool ran
// unasked, and a `deny` rule the USER wrote in config.json was silently
// unenforced — the one host where their own rules did not mean what they say.
// These tests pin the fix from the outside, through sdk.New, so a refactor
// that drops the wiring fails here rather than in a user's embedding.

// denyConfig writes a scratch $TERVA_HOME whose config denies bash, and
// returns nothing — the env var is the fixture.
func denyConfig(t *testing.T) {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	cfg := map[string]any{
		"provider":    "anthropic",
		"api_key":     "sk-test-not-used",
		"permissions": []map[string]string{{"tool": "bash", "decision": "deny", "reason": "no shell in this embedding"}},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// newRuntime builds a real Runtime. The credential is supplied here rather
// than left to the resolver chain: a Runtime that fails to construct would
// SKIP these tests, and a skipped guard is one that proves nothing while
// looking green.
func newRuntime(t *testing.T, cfg Config) *Runtime {
	t.Helper()
	provider.SetUserModels(nil)
	cfg.CWD = testsupport.TempDir(t)
	cfg.Provider = "anthropic"
	cfg.APIKey = "sk-test-no-request-is-ever-made"
	cfg.NoTools = false
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("sdk.New: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

// A user's deny rule holds in an embedding, like it does everywhere else.
func TestUserDenyRuleReachesTheEmbeddedAgent(t *testing.T) {
	denyConfig(t)
	rt := newRuntime(t, Config{})
	if rt.agent.BeforeToolExecute == nil {
		t.Fatal("sdk.New built an agent with no tool gate — a user's permission rules are unenforced")
	}
	allowed, reason, _ := rt.agent.BeforeToolExecute(provider.ToolCallBlock{
		Name: "bash", Arguments: json.RawMessage(`{"command":"echo hi"}`),
	})
	if allowed {
		t.Error("a denied tool was allowed to run in an SDK embedding")
	}
	if !strings.Contains(strings.ToLower(reason), "deny") && !strings.Contains(reason, "no shell") {
		t.Errorf("refusal reason does not explain itself to the model: %q", reason)
	}
}

// Nobody to ask means refuse, not run: the same fail-closed posture the other
// headless hosts take. `ask` here comes from the mode, not a rule.
func TestAnAskWithNoConfirmerFailsClosed(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	cfg := map[string]any{
		"provider": "anthropic", "api_key": "sk-test-not-used",
		"approval":    "ask",
		"permissions": []map[string]string{{"tool": "write", "decision": "ask"}},
	}
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(home, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	rt := newRuntime(t, Config{})
	if rt.agent.BeforeToolExecute == nil {
		t.Fatal("no gate installed")
	}
	allowed, reason, _ := rt.agent.BeforeToolExecute(provider.ToolCallBlock{
		Name: "write", Arguments: json.RawMessage(`{"path":"/tmp/x","content":"y"}`),
	})
	if allowed {
		t.Error("a call needing confirmation ran with no confirmer to answer it — the SDK must fail closed")
	}
	if reason == "" {
		t.Error("refusal carried no model-readable reason")
	}
}

// A supplied Confirmer is consulted, and its answer is honored.
type recordingConfirmer struct {
	asked  []string
	answer bool
}

func (c *recordingConfirmer) Confirm(toolName, preview string) core.ConfirmDecision {
	c.asked = append(c.asked, toolName)
	return core.ConfirmDecision{Allow: c.answer}
}

func TestASuppliedConfirmerIsConsulted(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	cfg := map[string]any{
		"provider": "anthropic", "api_key": "sk-test-not-used",
		"approval":    "ask",
		"permissions": []map[string]string{{"tool": "write", "decision": "ask"}},
	}
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(home, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	conf := &recordingConfirmer{answer: true}
	rt := newRuntime(t, Config{Confirmer: conf})
	allowed, _, _ := rt.agent.BeforeToolExecute(provider.ToolCallBlock{
		Name: "write", Arguments: json.RawMessage(`{"path":"/tmp/x","content":"y"}`),
	})
	if len(conf.asked) == 0 {
		t.Fatal("the supplied Confirmer was never consulted")
	}
	if !allowed {
		t.Error("the Confirmer allowed the call but the gate refused it")
	}
}

// Yolo is the named escape hatch: rules do not apply.
func TestYoloOptsOutOfUserRules(t *testing.T) {
	denyConfig(t)
	rt := newRuntime(t, Config{Yolo: true})
	if rt.agent.BeforeToolExecute == nil {
		return // no gate at all is the intended yolo shape
	}
	allowed, _, _ := rt.agent.BeforeToolExecute(provider.ToolCallBlock{
		Name: "bash", Arguments: json.RawMessage(`{"command":"echo hi"}`),
	})
	if !allowed {
		t.Error("Yolo:true still enforced the user's deny rule")
	}
}
