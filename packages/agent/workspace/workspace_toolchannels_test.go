package workspace

import (
	"reflect"
	"sort"
	"testing"

	"terva.sh/terva/packages/core"
)

// The rule in workspace_toolchannels.go, enforced without naming the channels.
//
// Three separate bugs have been the same bug: a channel that lives on a TOOL
// INSTANCE, dropped by the rebuild that mints fresh instances. Each was fixed by
// adding one more line beside the last one, so the fourth would have been found
// the same way — by a user, in a session, with the feature silently dead.
//
// So this asserts the PROPERTY instead: no function-valued field that was bound
// at session build may be nil after a rebuild. A new channel enrolls itself,
// because it will be bound at build and this notices if the rebuild forgets it.
// Nothing here knows what an asker or a host call is.
//
// Nil-at-build is not interesting — plenty of hooks are legitimately unset, and
// demanding they all be bound would be a different (and wrong) assertion. What
// is interesting is a binding that EXISTED and then went away.
func boundFuncFields(t *testing.T, ag *core.Agent) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for name, tl := range ag.ToolsSnapshot() {
		v := reflect.ValueOf(tl)
		if v.Kind() != reflect.Ptr || v.IsNil() {
			continue
		}
		v = v.Elem()
		if v.Kind() != reflect.Struct {
			continue
		}
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			// Func AND interface: a channel is bound as either, and which one
			// is an implementation detail of the tool. code_execution's HostCall
			// is a func; ask_user_question's Asker and the escalator are
			// interfaces. Checking only funcs made this test see nothing at all
			// in a build without terva_scripting — caught by the emptiness
			// check below, which is the whole reason it is there.
			//
			// Exported only: an unexported field cannot be read through
			// reflection without unsafe, and every channel in question is
			// exported precisely because a host has to bind it.
			if f.Kind() != reflect.Func && f.Kind() != reflect.Interface {
				continue
			}
			if !v.Type().Field(i).IsExported() {
				continue
			}
			if !f.IsNil() {
				out[name+"."+v.Type().Field(i).Name] = true
			}
		}
	}
	return out
}

func TestARebuildKeepsEveryToolChannel(t *testing.T) {
	s := newAskSession(t, "channel-rebuild")

	// The session fixture builds the registry the way buildSession does. Bind
	// the agent-side half too, which is what buildSession's bindAgentChannels
	// does — without it there is no "before" to lose.
	s.gate = core.NewConfirmGate(nil)
	s.bindAgentChannels(s.agent, s.gate)

	before := boundFuncFields(t, s.agent)
	if len(before) == 0 {
		t.Fatal("no bound channels found at session build; the reflection walk is not seeing the tools")
	}

	// Every reason rebuildTools fires with in real operation.
	for _, reason := range []string{
		"extension-context", "tool-withdrawal", "extension-reload",
		"approval-mode", "trust", "user-persona", "cast",
	} {
		s.rebuildTools(reason)
		after := boundFuncFields(t, s.agent)
		var lost []string
		for k := range before {
			if !after[k] {
				lost = append(lost, k)
			}
		}
		if len(lost) > 0 {
			sort.Strings(lost)
			t.Fatalf("rebuildTools(%q) dropped %d tool channel(s): %v\n"+
				"A fresh Resolve mints fresh tool instances; anything bound onto one has to be\n"+
				"re-bound in bindResolvedChannels or bindAgentChannels (workspace_toolchannels.go).",
				reason, len(lost), lost)
		}
	}
}
