package permissions

import (
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/mode"
	"terva.sh/terva/packages/core/permission"
	"terva.sh/terva/packages/testsupport"
)

// 🔑 A worker on the Talkoot bridge lets the bridge's tools through in every
// posture, because the daemon is their one gate. A worker rule for mcp_* does
// not stop them, and a planning worker keeps them.
func TestTheWorkerLetsTheBridgeToolsThrough(t *testing.T) {
	withTempHome(t)
	cfg := config.Config{Permissions: []config.PermissionRuleConfig{{Tool: "mcp_*", Decision: "deny"}}}
	for _, posture := range []string{"plan", "ask", "auto-edit", "workspace", "yolo"} {
		in := Inputs{Mode: mode.RPC, CWD: testsupport.TempDir(t), Approval: posture, TeamBridge: true}
		pol, _, err := policyFromConfig(in, cfg)
		if err != nil || pol == nil {
			t.Fatalf("%s: policy %v, %v", posture, pol, err)
		}
		for _, name := range TeamBridgeTools() {
			if v, reason := pol.Evaluate(name, []byte(`{}`)); v != permission.VerdictAllow {
				t.Errorf("%s: %s = %v (%s), want allow", posture, name, v, reason)
			}
		}
		// The rules name these tools alone.
		if v, _ := pol.Evaluate("mcp_other_send", []byte(`{}`)); v != permission.VerdictDeny {
			t.Errorf("%s: another MCP tool = %v, want the worker's deny", posture, v)
		}
	}
}

// A run without the bridge treats the same names as any foreign MCP tool.
func TestTheBridgeToolsNeedTheBridge(t *testing.T) {
	withTempHome(t)
	in := Inputs{Mode: mode.RPC, CWD: testsupport.TempDir(t), Approval: "ask"}
	pol, _, err := policyFromConfig(in, config.Config{})
	if err != nil || pol == nil {
		t.Fatalf("policy %v, %v", pol, err)
	}
	if v, _ := pol.Evaluate(TeamBridgeTools()[0], []byte(`{}`)); v != permission.VerdictAsk {
		t.Fatalf("ask posture without the bridge = %v, want ask", v)
	}
	in.Approval = "plan"
	pol, _, _ = policyFromConfig(in, config.Config{})
	if v, _ := pol.Evaluate(TeamBridgeTools()[0], []byte(`{}`)); v != permission.VerdictDeny {
		t.Fatalf("plan posture without the bridge = %v, want deny", v)
	}
}

func TestTeamBridgeToolNames(t *testing.T) {
	got := TeamBridgeTools()
	want := []string{"mcp_terva_talkoot_talkoot_send", "mcp_terva_talkoot_talkoot_handoff", "mcp_terva_talkoot_talkoot_roster", "mcp_terva_talkoot_ask_user_question"}
	if len(got) != len(want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("names = %v, want %v", got, want)
		}
	}
}
