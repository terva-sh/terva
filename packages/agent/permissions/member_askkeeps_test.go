package permissions

import (
	"encoding/json"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/mode"
	"terva.sh/terva/packages/core/permission"
	"terva.sh/terva/packages/testsupport"
)

// routinesOn writes a user configuration with Talkoot and routines on. The
// member's rule reads that key at each call.
func routinesOn(t *testing.T, on bool) {
	t.Helper()
	enabled := true
	cfg := config.Config{TalkootEnabled: &enabled}
	if on {
		cfg.TalkootRoutines = &config.TalkootRoutinesConfig{Max: 5}
	}
	if err := config.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if got := config.TalkootRoutines().On; got != on {
		t.Fatalf("routines are on = %v after the write, want %v", got, on)
	}
}

// memberPolicy is the policy of a Talkoot member in one posture, with the
// user's rules in cfg, in a home where routines are on.
func memberPolicy(t *testing.T, posture string, cfg config.Config) *permission.PermissionPolicy {
	t.Helper()
	withTempHome(t)
	routinesOn(t, true)
	in := Inputs{Mode: mode.Web, CWD: testsupport.TempDir(t), Approval: posture, TalkootMember: true}
	pol, _, err := policyFromConfig(in, cfg)
	if err != nil || pol == nil {
		t.Fatalf("policy in %s: %v, %v", posture, pol, err)
	}
	return pol
}

func sendTo(to ...string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"to": to, "kind": "message", "body": "The check ran."})
	return b
}

// A member in ask or auto-edit asks for a routine, and writes to the person,
// with no approval card (TKT-01M7TCSS9). With nobody present a card ends the
// work at its deadline, so a routine's member could not say what a run found.
func TestAMemberAsksForARoutineAndWritesToThePersonWithNoCard(t *testing.T) {
	routine := json.RawMessage(`{"kind":"task","schedule":"every 15m","prompt":"List the open pull requests."}`)
	for _, posture := range []string{"ask", "auto-edit"} {
		pol := memberPolicy(t, posture, config.Config{})
		for name, call := range map[string]struct {
			tool string
			args json.RawMessage
		}{
			"a routine request":           {"talkoot_routine", routine},
			"a send to the word":          {"talkoot_send", sendTo("human")},
			"a send to a person address":  {"talkoot_send", sendTo("human:sothr")},
			"a send to both person forms": {"talkoot_send", sendTo("human", "human:sothr")},
		} {
			if v, why := pol.Evaluate(call.tool, call.args); v != permission.VerdictAllow {
				t.Errorf("%s, %s: got %v (%s), want allow", posture, name, v, why)
			}
		}
	}
}

// 🚨 An envelope to a member starts that member's turn, in its posture and on
// its budget. So a send with a member among its recipients still asks, and so
// does every send that the gate cannot read as one to the person alone.
func TestAMemberSendThatReachesAMemberStillAsks(t *testing.T) {
	for _, posture := range []string{"ask", "auto-edit"} {
		pol := memberPolicy(t, posture, config.Config{})
		for name, args := range map[string]json.RawMessage{
			"a member":                     sendTo("developer"),
			"the person and a member":      sendTo("human", "developer"),
			"a member and then the person": sendTo("developer", "human:sothr"),
			// A member can have a person's bare name as its id, and the
			// member id wins in the router.
			"a bare name":                  sendTo("sothr"),
			"no recipient":                 sendTo(),
			"the word in upper case":       sendTo("Human"),
			"a word that starts so":        sendTo("humane"),
			"to is no list":                json.RawMessage(`{"to":"human","body":"x"}`),
			"no to":                        json.RawMessage(`{"body":"to: human"}`),
			"arguments that are no object": json.RawMessage(`["human"]`),
			// The decoder keeps the last of two keys, as the tool does.
			"to named two times, a member last": json.RawMessage(`{"to":["human"],"to":["developer"],"body":"x"}`),
		} {
			if v, _ := pol.Evaluate("talkoot_send", args); v != permission.VerdictAsk {
				t.Errorf("%s, %s: got %v, want ask", posture, name, v)
			}
		}
		// The sibling tools keep their card. A handoff passes work to a
		// member, and a proposal was not part of the decision.
		for _, tool := range []string{"talkoot_handoff", "talkoot_propose", "talkoot_wake", "bash"} {
			if v, _ := pol.Evaluate(tool, sendTo("human")); v != permission.VerdictAsk {
				t.Errorf("%s: %s got %v, want ask", posture, tool, v)
			}
		}
	}
}

// A rule that a person wrote decides the call before the posture does.
func TestAPersonsRuleStillDecidesAMembersPersonFacingCall(t *testing.T) {
	for _, posture := range []string{"ask", "auto-edit"} {
		for _, tool := range []string{"talkoot_routine", "talkoot_send"} {
			ask := memberPolicy(t, posture, config.Config{Permissions: []config.PermissionRuleConfig{{Tool: tool, Decision: "ask"}}})
			if v, _ := ask.Evaluate(tool, sendTo("human")); v != permission.VerdictAsk {
				t.Errorf("%s, %s with an ask rule: got %v, want ask", posture, tool, v)
			}
			deny := memberPolicy(t, posture, config.Config{Permissions: []config.PermissionRuleConfig{{Tool: tool, Decision: "deny"}}})
			if v, _ := deny.Evaluate(tool, sendTo("human")); v != permission.VerdictDeny {
				t.Errorf("%s, %s with a deny rule: got %v, want deny", posture, tool, v)
			}
		}
	}
}

// 🚨 The rule holds only while routines are on in the user configuration. A
// person who never turned them on keeps every card of ask and auto-edit, and
// turning them off brings the cards back at the next call, with no restart.
func TestAMemberKeepsEveryCardWhileRoutinesAreOff(t *testing.T) {
	routine := json.RawMessage(`{"kind":"task","schedule":"every 15m","prompt":"List the open pull requests."}`)
	for _, posture := range []string{"ask", "auto-edit"} {
		pol := memberPolicy(t, posture, config.Config{})
		routinesOn(t, false)
		if v, _ := pol.Evaluate("talkoot_routine", routine); v != permission.VerdictAsk {
			t.Errorf("%s, routines off: a routine request got %v, want ask", posture, v)
		}
		if v, _ := pol.Evaluate("talkoot_send", sendTo("human")); v != permission.VerdictAsk {
			t.Errorf("%s, routines off: a send to the person got %v, want ask", posture, v)
		}
		// The same policy, with the key on again.
		routinesOn(t, true)
		if v, _ := pol.Evaluate("talkoot_send", sendTo("human")); v != permission.VerdictAllow {
			t.Errorf("%s, routines on again: a send to the person got %v, want allow", posture, v)
		}
	}
}

// The gate reads the recipients with the tool's own decode. So a payload
// that names a member under a key in another case gets the card, as the tool
// would send to that member.
func TestTheGateReadsTheRecipientsThatTheToolReads(t *testing.T) {
	pol := memberPolicy(t, "auto-edit", config.Config{})
	for name, args := range map[string]json.RawMessage{
		"a member under an upper case key, last":  json.RawMessage(`{"to":["human"],"TO":["developer"],"body":"x"}`),
		"a member under a mixed case key, last":   json.RawMessage(`{"to":["human"],"To":["developer"],"body":"x"}`),
		"the person last, and a member in a list": json.RawMessage(`{"To":["developer"],"to":["human","developer"],"body":"x"}`),
	} {
		if v, _ := pol.Evaluate("talkoot_send", args); v != permission.VerdictAsk {
			t.Errorf("%s: got %v, want ask", name, v)
		}
	}
	// The other order sends to the person alone, in the tool and here.
	if v, _ := pol.Evaluate("talkoot_send", json.RawMessage(`{"TO":["developer"],"to":["human"],"body":"x"}`)); v != permission.VerdictAllow {
		t.Errorf("the person under the last key: got %v, want allow", v)
	}
}

// The rule belongs to a member. A session with no seat has neither tool, and
// its policy must not carry a rule that an extension tool of the same name
// could use.
func TestASessionWithNoSeatKeepsNoPersonFacingCall(t *testing.T) {
	withTempHome(t)
	routinesOn(t, true)
	in := Inputs{Mode: mode.Web, CWD: testsupport.TempDir(t), Approval: "auto-edit"}
	pol, _, err := policyFromConfig(in, config.Config{})
	if err != nil || pol == nil {
		t.Fatalf("policy: %v, %v", pol, err)
	}
	for _, tool := range []string{"talkoot_routine", "talkoot_send"} {
		if v, _ := pol.Evaluate(tool, sendTo("human")); v != permission.VerdictAsk {
			t.Errorf("%s with no seat: got %v, want ask", tool, v)
		}
	}
}
