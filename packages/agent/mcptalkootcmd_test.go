package agent

import (
	"bytes"
	"testing"

	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/tools"
)

// TestTalkootBridgeServesTheNativeText pins that the bridge lists exactly
// talkoot.BridgeTools, each with the description and schema a native member
// reads, ask_user_question included, so a foreign model reads what a native
// one reads.
func TestTalkootBridgeServesTheNativeText(t *testing.T) {
	native := map[string]tools.TalkootToolDef{}
	for _, d := range tools.TalkootToolDefs() {
		native[d.Name] = d
	}
	ask := &tools.AskUserTool{}
	native[ask.Name()] = tools.TalkootToolDef{Name: ask.Name(), Description: ask.Description(), Schema: ask.Schema()}
	got := bridgeTeamTools()
	if len(got) != len(talkoot.BridgeTools) {
		t.Fatalf("the bridge serves %d tools, want %d", len(got), len(talkoot.BridgeTools))
	}
	for i, tl := range got {
		if tl.Name != talkoot.BridgeTools[i] {
			t.Errorf("tool %d = %q, want %q", i, tl.Name, talkoot.BridgeTools[i])
		}
		d, ok := native[tl.Name]
		if !ok {
			t.Errorf("the bridge serves %q, which no native tool is", tl.Name)
			continue
		}
		if tl.Description != d.Description || !bytes.Equal(tl.Schema, d.Schema) || tl.Description == "" {
			t.Errorf("%s differs from the native tool's text", tl.Name)
		}
	}
}
