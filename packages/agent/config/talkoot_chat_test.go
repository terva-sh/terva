package config

import (
	"reflect"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// 🚨 The key decides who may write in the person's name, so only the user
// layer sets it. A project file that names a bridge must change nothing, for
// a trusted project too.
func TestTalkootChatIsAUserLayerKey(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	cwd := testsupport.TempDir(t)
	on := true
	if err := SaveConfig(Config{TalkootEnabled: &on}); err != nil {
		t.Fatal(err)
	}
	writeProjectConfig(t, cwd, `{"disable_extensions":["positive-control"],"talkoot_chat":{"talkoot":"crew","connector":"telegram","person":"mallory"}}`)
	for _, trusted := range []bool{false, true} {
		eff := ResolveConfig(cwd, trusted)
		if len(eff.DisableExtensions) != 1 || eff.DisableExtensions[0] != "positive-control" {
			t.Fatal("the project fixture did not reach the resolver")
		}
		if eff.TalkootChat != nil || TalkootChat() != nil {
			t.Fatalf("a project config (trusted=%v) turned the chat bridge on", trusted)
		}
	}

	want := TalkootChatConfig{Talkoot: "crew", Connector: "telegram", Person: "sothr", Brief: []string{"07:30", "19:00"}, Quiet: "22:00-07:00"}
	if err := SaveConfig(Config{TalkootEnabled: &on, TalkootChat: &want}); err != nil {
		t.Fatal(err)
	}
	if got := TalkootChat(); got == nil || !reflect.DeepEqual(*got, want) {
		t.Fatalf("TalkootChat() = %+v, want %+v from the user layer", got, want)
	}
}

// A bridge to a team the daemon does not load serves nothing, and a key with
// no team names nothing to serve.
func TestTalkootChatNeedsTalkootAndATeam(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if TalkootChat() != nil {
		t.Fatal("an empty config has a chat bridge")
	}
	on, off := true, false
	chat := &TalkootChatConfig{Talkoot: "crew", Person: "sothr"}
	if err := SaveConfig(Config{TalkootEnabled: &off, TalkootChat: chat}); err != nil {
		t.Fatal(err)
	}
	if TalkootChat() != nil {
		t.Fatal("the bridge is on while talkoot_enabled is off")
	}
	if err := SaveConfig(Config{TalkootEnabled: &on, TalkootChat: &TalkootChatConfig{Person: "sothr"}}); err != nil {
		t.Fatal(err)
	}
	if TalkootChat() != nil {
		t.Fatal("a key that names no team turned the bridge on")
	}
}

// 🚨 TalkootChat returns nil for a refused key, as it does for an unset one.
// The daemon and the chat pane read this text to say that a key that a
// person set starts no bridge.
func TestTalkootChatProblemSaysWhyTheKeyStartsNoBridge(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if got := TalkootChatProblem(); got != "" {
		t.Fatalf("an empty config has the problem %q", got)
	}
	on, off := true, false
	chat := &TalkootChatConfig{Talkoot: "crew", Person: "sothr"}
	for _, c := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"talkoot off", Config{TalkootEnabled: &off, TalkootChat: chat}, "talkoot_chat is set, and talkoot_enabled is off"},
		{"talkoot unset", Config{TalkootChat: chat}, "talkoot_chat is set, and talkoot_enabled is off"},
		{"no team", Config{TalkootEnabled: &on, TalkootChat: &TalkootChatConfig{Person: "sothr"}}, "talkoot_chat is set, and it names no talkoot"},
		{"in use", Config{TalkootEnabled: &on, TalkootChat: chat}, ""},
		{"key unset", Config{TalkootEnabled: &off}, ""},
	} {
		if err := SaveConfig(c.cfg); err != nil {
			t.Fatal(err)
		}
		if got := TalkootChatProblem(); got != c.want {
			t.Errorf("%s: TalkootChatProblem() = %q, want %q", c.name, got, c.want)
		}
		// The two readers must agree: a key with a problem starts no bridge,
		// and a key with none does when it is set.
		if c.want != "" && TalkootChat() != nil {
			t.Errorf("%s: the key has a problem and TalkootChat still returns it", c.name)
		}
	}
}
