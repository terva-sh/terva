package build

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

type generationSource struct{ infos []ExtensionToolInfo }

func (s generationSource) Tools() []ExtensionToolInfo                   { return s.infos }
func (s generationSource) NewExtensionTool(ExtensionToolInfo) core.Tool { return echoTool{} }

type rebuildAsker struct{}

func (rebuildAsker) Ask(context.Context, []core.UserQuestion) ([]core.UserAnswer, error) {
	return nil, nil
}

func TestLiveToolSetCarriesAskerAcrossRebuild(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	args := Args{
		Provider: "openai",
		Model:    "gpt-5",
		CWD:      testsupport.TempDir(t),
		NoExt:    true,
		NoMCP:    true,
	}
	r, err := Resolve(args, true)
	if err != nil {
		t.Fatal(err)
	}
	asker := rebuildAsker{}
	ag := r.NewAgent()
	LiveToolSet{Args: args, Asker: asker}.Rebuild(ag)
	tool, ok := ag.LookupTool("ask_user_question")
	if !ok {
		t.Fatal("rebuilt registry has no ask_user_question tool")
	}
	askTool, ok := tool.(*tools.AskUserTool)
	if !ok || askTool.Asker == nil {
		t.Fatalf("rebuilt ask_user_question lost its Asker: %#v", tool)
	}
}

func TestToolGenerationReclassifiesSameName(t *testing.T) {
	for _, mode := range []core.ApprovalMode{core.ApprovalWorkspace, core.ApprovalAutoEdit, core.ApprovalPlan} {
		t.Run(string(mode), func(t *testing.T) {
			pol := permissions.NewPolicy(mode, nil)
			gate := core.NewPolicyGate(pol, nil)
			ag := core.NewAgent(nil, "fake", "", nil)
			for _, state := range []string{"read", "mutate", "removed", "mutate", "read", "network"} {
				r := Resolved{ToolRegistry: core.Registry{}, ApprovalMode: mode, readOnlySet: permissions.BuiltinReadOnlySet()}
				info := ExtensionToolInfo{Name: "echo", ReadOnly: state == "read" || state == "network"}
				if state == "network" {
					info.Authority = string(core.AuthNetworkRead)
				}
				if state != "removed" {
					r.MergeExtensionTools(generationSource{[]ExtensionToolInfo{info}})
				}
				r.PublishTools(ag, gate)
				ctx, _, exists := ag.ToolForCall(context.Background(), "echo")
				wantExists := state != "removed" && (mode != core.ApprovalPlan || state == "read")
				if exists != wantExists {
					t.Fatalf("%s: registered=%v want=%v", state, exists, wantExists)
				}
				allowed, _, _ := gate.Check(ctx, "echo", nil, "", "")
				if allowed != (state == "read") {
					t.Fatalf("%s: allowed=%v", state, allowed)
				}
			}
		})
	}
}
