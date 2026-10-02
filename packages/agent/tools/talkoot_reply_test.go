package tools

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/testsupport"
)

type personReplySeat struct {
	fakeSeat
	router *talkoot.Router
}

func (s *personReplySeat) Send(o talkoot.Outgoing) (talkoot.Envelope, error) {
	return s.router.Send("helm", o)
}

type personReplyDriver struct{ deliveries int }

func (d *personReplyDriver) Deliver(_ string, _ talkoot.Member, _ string) error {
	d.deliveries++
	return nil
}

func TestTalkootSendPersonReplyUsesRouterIdentity(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := talkoot.CreateRoom(dir); err != nil {
		t.Fatal(err)
	}
	r := talkoot.Roster{ID: "crew", BudgetUSDPerDay: 5, Members: []talkoot.Member{{ID: "helm", Role: talkoot.RoleCoordinator, Driver: talkoot.DriverNative}}}
	d := &personReplyDriver{}
	rt, err := talkoot.NewRouter(r, talkoot.OpenRoom(dir), talkoot.Drivers{Native: d, Worker: d}, talkoot.DefaultLimits(), nil)
	if err != nil {
		t.Fatal(err)
	}
	root, err := rt.Post("Drew", nil, "Hello.", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	tool := &TalkootSendTool{Seat: &personReplySeat{router: rt}}
	text, err := talkootText(t, tool, `{"to":["Drew"],"kind":"message","body":"Hello back."}`)
	if err != nil || !strings.Contains(text, "to human:Drew.") {
		t.Fatalf("person reply result: %q, %v", text, err)
	}
	lines, err := talkoot.OpenRoom(dir).Read()
	if err != nil {
		t.Fatal(err)
	}
	var reply *talkoot.Envelope
	for _, l := range lines {
		if l.Type == talkoot.LineEnvelope && l.Envelope.From == "helm" {
			reply = l.Envelope
		}
	}
	if reply == nil || reply.Chain.Root != root.ID || !slices.Equal(reply.To, []string{"human:Drew"}) || d.deliveries != 1 {
		t.Fatalf("tool bypassed router identity or delivered to person: %+v, deliveries %d", reply, d.deliveries)
	}
	if _, err := talkootText(t, tool, `{"to":["human:Alex"],"kind":"message","body":"spoof"}`); err == nil {
		t.Fatal("tool created an arbitrary person")
	}
}

func TestTalkootSendDefinesBoundedPersonReply(t *testing.T) {
	tool := &TalkootSendTool{}
	if !strings.Contains(tool.Description(), "person at this chain's root") || !strings.Contains(tool.Description(), "wakes nobody") {
		t.Fatalf("description omits bounded person replies: %s", tool.Description())
	}
	var schema struct {
		Properties map[string]struct{ Description string }
	}
	if err := json.Unmarshal(tool.Schema(), &schema); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(schema.Properties["to"].Description, "human") || !strings.Contains(schema.Properties["to"].Description, "Member ids take precedence") {
		t.Fatalf("to does not explain the human alias: %+v", schema.Properties["to"])
	}
	if strings.Contains(string((&TalkootHandoffTool{}).Schema()), "person at this chain's root") {
		t.Fatal("person reply schema widened handoff recipients")
	}
}
