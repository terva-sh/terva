package workspace

import (
	"testing"

	"terva.sh/terva/packages/agent/config"
)

// 🚨 A talkoot_chat key that starts no bridge left the chat pane at idle, with
// no reason. The pane is the one place in the app that a person can read the
// reason while Talkoot is off, because no team has a view then.
func TestTheChatViewSaysWhyATeamBridgeDidNotStart(t *testing.T) {
	w, _, _ := chatTestWorkspace(t, "s1")
	on, off := true, false
	key := &config.TalkootChatConfig{Talkoot: "crew", Connector: "telegram", Person: "sothr"}

	if err := config.SaveConfig(config.Config{TalkootEnabled: &off, TalkootChat: key}); err != nil {
		t.Fatal(err)
	}
	v := w.chatView()
	if want := "talkoot_chat is set, and talkoot_enabled is off"; v.Problem != want {
		t.Fatalf("the chat view has the problem %q, want %q", v.Problem, want)
	}
	if v.Bridge.State != chatStateIdle {
		t.Fatalf("the bridge is %q with the key refused, want idle", v.Bridge.State)
	}

	if err := config.SaveConfig(config.Config{TalkootEnabled: &on, TalkootChat: key}); err != nil {
		t.Fatal(err)
	}
	if got := w.chatView().Problem; got != "" {
		t.Fatalf("the chat view has the problem %q for a key in use", got)
	}

	if err := config.SaveConfig(config.Config{TalkootEnabled: &off}); err != nil {
		t.Fatal(err)
	}
	if got := w.chatView().Problem; got != "" {
		t.Fatalf("the chat view has the problem %q with no key set", got)
	}
}
