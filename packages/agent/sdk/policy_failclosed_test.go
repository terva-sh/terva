package sdk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/modelreg"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// A config.json that cannot be parsed takes the user's permission rules and
// approval mode with it. sdk.New used to discard that failure, find no policy
// under the headless yolo default, and hand back a runtime whose agent ran
// every tool unchecked: the silent ungated host the required gate exists to
// prevent, in the one host built for outside code.
func TestAnUnreadableConfigFailsClosed(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	cfgPath := filepath.Join(home, "config.json")
	// A deny rule the user wrote, in a file one typo away from parsing.
	broken := `{"permissions": [{"tool": "bash", "decision": "deny", "reason": "no shell here"}],}`
	if err := os.WriteFile(cfgPath, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	modelreg.SetUserModels(nil)
	base := Config{CWD: testsupport.TempDir(t), Provider: "anthropic", APIKey: "sk-test-no-request-is-ever-made"}

	rt, err := New(base)
	if err == nil {
		_ = rt.Close()
		t.Fatal("sdk.New built a runtime although the user's permission rules could not be read")
	}
	if rt != nil {
		t.Error("sdk.New returned a runtime alongside its error")
	}

	// Yolo is the named way out, and it still works.
	yolo := base
	yolo.Yolo = true
	rt, err = New(yolo)
	if err != nil {
		t.Fatalf("Yolo:true must not depend on the user's config: %v", err)
	}
	_ = rt.Close()

	// The remedy the error names: repair the file. The rule it holds then
	// applies, which is the point of refusing rather than running without it.
	if err := os.WriteFile(cfgPath, []byte(`{"permissions": [{"tool": "bash", "decision": "deny", "reason": "no shell here"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rt, err = New(base)
	if err != nil {
		t.Fatalf("sdk.New still fails after the config was repaired: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	allowed, _, _ := rt.agent.Gate().CheckTool(t.Context(), provider.ToolCallBlock{
		Name: "bash", Arguments: json.RawMessage(`{"command":"echo hi"}`),
	}, nil)
	if allowed {
		t.Error("the repaired config's deny rule did not reach the agent's gate")
	}
	if rt.agent.Gate() == core.Gate(core.AllowAll) {
		t.Error("a runtime with a policy was built with AllowAll")
	}
}

// The same failure one layer down. A project's .terva/config.json holds deny
// and ask rules that can only tighten, and a parse error used to drop them
// with no signal at all, so an embedded agent ran the project's forbidden
// tools. sdk.New now refuses, naming the file, and runs once it is repaired.
func TestAnUnreadableProjectConfigFailsClosed(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	cwd := testsupport.TempDir(t)
	dir := filepath.Join(cwd, ".terva")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"permissions": [{"tool": "bash", "decision": "deny"},]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	modelreg.SetUserModels(nil)
	base := Config{CWD: cwd, Provider: "anthropic", APIKey: "sk-test-no-request-is-ever-made"}

	rt, err := New(base)
	if err == nil {
		_ = rt.Close()
		t.Fatal("sdk.New built a runtime although the project's permission rules could not be read")
	}
	if !strings.Contains(err.Error(), "project config unreadable") || !strings.Contains(err.Error(), path) {
		t.Errorf("sdk.New failed, but not for the project config: %v", err)
	}

	if err := os.WriteFile(path, []byte(`{"permissions": [{"tool": "bash", "decision": "deny"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rt, err = New(base)
	if err != nil {
		t.Fatalf("sdk.New still fails after the project config was repaired: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	allowed, _, _ := rt.agent.Gate().CheckTool(t.Context(), provider.ToolCallBlock{
		Name: "bash", Arguments: json.RawMessage(`{"command":"echo hi"}`),
	}, nil)
	if allowed {
		t.Error("the repaired project config's deny rule did not reach the agent's gate")
	}
}
