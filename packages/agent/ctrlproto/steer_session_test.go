package ctrlproto

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// notSessionParams are params fields that look as if they name a session and
// do not name the verb's target, each with the reason. The census below makes
// every such field land here or in paramSessionMethods.
var notSessionParams = map[string]string{
	"workflows.get.id":       "a workflow id",
	"shared.fetch.id":        "a shared file of the frame's session",
	"sidechat.ask.id":        "a side chat of the frame's session",
	"sidechat.close.id":      "a side chat of the frame's session",
	"context.node.id":        "a context node of the frame's session",
	"surface.get.id":         "a surface of the frame's session",
	"surface.action.id":      "a surface of the frame's session",
	"cards.doctor.session":   "a scene that grounds a pass over a card; the card is the target",
	"worlds.doctor.sessions": "scenes that ground a pass over a world; the world is the target",
}

// paramsFields returns the top-level json fields of a params type, through a
// pointer and through embedded structs, whose fields json lifts to the top.
// A named struct field is another object, so its fields are not the verb's.
func paramsFields(rt reflect.Type) []reflect.StructField {
	for rt != nil && rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt == nil || rt.Kind() != reflect.Struct {
		return nil
	}
	var out []reflect.StructField
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Anonymous && f.Tag.Get("json") == "" {
			out = append(out, paramsFields(f.Type)...)
			continue
		}
		out = append(out, f)
	}
	return out
}

// jsonName is the key json gives f: its tag's name, else the field name,
// which json matches in any case. A field json skips has no key.
func jsonName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	switch {
	case name == "-":
		return ""
	case name == "":
		return strings.ToLower(f.Name)
	}
	return name
}

// sessionishParam reports whether a params field may name a session: a json
// name about sessions in any group, or an id on a session or conversation verb.
func sessionishParam(m Method, name string) bool {
	if strings.Contains(name, "sess") {
		return true
	}
	g := m.Group()
	return (name == "id" || name == "ids" || name == "members") && (g == GroupSession || g == GroupConversation)
}

// The steer rule finds a verb's target through the two tables in
// steer_session.go. This drives every verb with a frame session, and fails
// when the verbs that act on that session differ from frameSessionMethods. It
// reads each params type, not the fixture's values, so a field that may name a
// session fails it until paramSessionMethods or notSessionParams accounts for
// the field.
func TestSteerRuleKnowsEverySessionVerb(t *testing.T) {
	cased := map[Method]bool{}
	// dispatchCases leaves out turn.swipe, whose one verb routes to two
	// methods. Both routes take the frame session, so one row stands for both.
	one := 0
	cases := append(dispatchCases(),
		dispatchCase{method: MethodTurnSwipe, params: TurnSwipeParams{}},
		dispatchCase{method: MethodTurnSwipe, params: TurnSwipeParams{Index: &one}})
	for _, tc := range cases {
		cased[tc.method] = true
		rec := &recorder{fakeSvc: &fakeSvc{}}
		s := &serveState{
			authority: capAll, svc: rec, contract: allGroups(),
			write: func(Frame) error { return nil },
			subs:  map[string]context.CancelFunc{},
		}
		raw, _ := json.Marshal(tc.params)
		if tc.params == nil {
			raw = nil
		}
		s.handle(context.Background(), Frame{Kind: KindCmd, ID: 1, Sess: dispatchSess, Method: tc.method, Params: raw})
		if forwards := rec.sess == dispatchSess; forwards != frameSessionMethods[tc.method] {
			t.Errorf("%s: forwards the frame session %v, but frameSessionMethods says %v", tc.method, forwards, frameSessionMethods[tc.method])
		}
		target, named := paramSessionMethods[tc.method]
		for _, f := range paramsFields(reflect.TypeOf(tc.params)) {
			name := jsonName(f)
			key := string(tc.method) + "." + name
			if sessionishParam(tc.method, name) && !named && notSessionParams[key] == "" {
				t.Errorf("%s field %s may name a session: give %s an entry in paramSessionMethods, or say why not in notSessionParams", reflect.TypeOf(tc.params), f.Name, tc.method)
			}
		}
		if named {
			id := target(raw)
			args, _ := json.Marshal(rec.args)
			if id == "" || !strings.Contains(string(args), id) {
				t.Errorf("%s: the extractor returns %q, and the handler received %s", tc.method, id, args)
			}
		}
	}
	for m := range frameSessionMethods {
		if !cased[m] {
			t.Errorf("%s is in frameSessionMethods and has no dispatch case", m)
		}
	}
	for key := range notSessionParams {
		if !cased[Method(key[:strings.LastIndex(key, ".")])] {
			t.Errorf("notSessionParams names %s, which has no dispatch case", key)
		}
	}
	for m := range paramSessionMethods {
		if !cased[m] {
			t.Errorf("%s is in paramSessionMethods and has no dispatch case", m)
		}
	}
}

// boundSvc is a recorder whose talkoot drives the sessions in bound.
type boundSvc struct {
	*recorder
	bound map[string]bool
	asked []string
}

func (b *boundSvc) SteersTalkoot(id string) bool {
	b.asked = append(b.asked, id)
	return b.bound[id]
}

// A write or a spend on a session a talkoot drives needs steer. A read does
// not, and neither does a write on a session no talkoot drives.
func TestABoundSessionNeedsSteerToWriteOrSpend(t *testing.T) {
	const member, mine = "20260101-120000-bbbbbbbb", "20260101-120000-cccccccc"
	type call struct {
		method Method
		sess   string
		params any
	}
	run := func(t *testing.T, authority Capability, c call) (*boundSvc, *Frame) {
		t.Helper()
		svc := &boundSvc{recorder: &recorder{fakeSvc: &fakeSvc{}}, bound: map[string]bool{member: true}}
		var reply *Frame
		s := &serveState{
			authority: authority, svc: svc, contract: allGroups(),
			write: func(f Frame) error {
				if f.Error != nil {
					reply = &f
				}
				return nil
			},
			subs: map[string]context.CancelFunc{},
		}
		raw, _ := json.Marshal(c.params)
		if c.params == nil {
			raw = nil
		}
		s.handle(context.Background(), Frame{Kind: KindCmd, ID: 1, Sess: c.sess, Method: c.method, Params: raw})
		return svc, reply
	}
	noSteer := capAll &^ CapSteer
	steers := []call{
		{MethodPrompt, member, PromptParams{Text: "hi"}},
		{MethodAnswer, member, AnswerParams{}},
		{MethodMessageEdit, member, MessageEditParams{}},
		{MethodSessionDelete, member, nil},
		{MethodCancel, member, nil},
		{MethodSessionRestore, "", RestoreSessionParams{ID: member}},
		// A talkoot's address names the carrier of its worker members' asks.
		{MethodApprove, TalkootAddr("crew"), ApproveParams{}},
	}
	for _, c := range steers {
		t.Run("refused/"+string(c.method), func(t *testing.T) {
			svc, reply := run(t, noSteer, c)
			if reply == nil || reply.Error.Code != CodeForbidden || !strings.Contains(reply.Error.Message, "talkoot drives this session") {
				t.Fatalf("%s on a bound session without steer: %+v", c.method, reply)
			}
			if svc.called != "" {
				t.Fatalf("%s reached the handler %s", c.method, svc.called)
			}
		})
		t.Run("steer/"+string(c.method), func(t *testing.T) {
			svc, reply := run(t, capAll, c)
			if reply != nil && reply.Error.Code == CodeForbidden {
				t.Fatalf("%s on a bound session with steer: %+v", c.method, reply.Error)
			}
			if len(svc.asked) != 0 {
				t.Fatalf("a caller with steer paid for the lookup: %v", svc.asked)
			}
		})
		t.Run("unbound/"+string(c.method), func(t *testing.T) {
			c := c
			if c.sess != "" {
				c.sess = mine
			} else {
				c.params = RestoreSessionParams{ID: mine}
			}
			if _, reply := run(t, noSteer, c); reply != nil && reply.Error.Code == CodeForbidden {
				t.Fatalf("%s on an unbound session: %+v", c.method, reply.Error)
			}
		})
	}
	t.Run("restore without an id", func(t *testing.T) {
		if _, reply := run(t, noSteer, call{MethodSessionRestore, "", RestoreSessionParams{}}); reply == nil || reply.Error.Code != CodeForbidden || !strings.Contains(reply.Error.Message, "name no session") {
			t.Fatalf("a restore that names no session, without steer: %+v", reply)
		}
	})
	for _, m := range []Method{MethodContextGet, MethodConversationHistory, MethodSessionState} {
		t.Run("read/"+string(m), func(t *testing.T) {
			svc, reply := run(t, CapRead, call{m, member, nil})
			if reply != nil && reply.Error.Code == CodeForbidden {
				t.Fatalf("%s on a bound session: %+v", m, reply.Error)
			}
			if len(svc.asked) != 0 {
				t.Fatalf("a read asked whether its session is bound: %v", svc.asked)
			}
		})
	}
}

// A carrier that cannot say which sessions a talkoot drives refuses a caller
// without steer every session write, and still serves reads and a caller
// with steer.
func TestTheSteerRuleFailsClosedWithoutATalkootService(t *testing.T) {
	run := func(authority Capability, m Method, params any) (*recorder, *Frame) {
		rec := &recorder{fakeSvc: &fakeSvc{}}
		var reply *Frame
		s := &serveState{
			authority: authority, svc: rec, contract: allGroups(),
			write: func(f Frame) error {
				if f.Error != nil {
					reply = &f
				}
				return nil
			},
			subs: map[string]context.CancelFunc{},
		}
		raw, _ := json.Marshal(params)
		s.handle(context.Background(), Frame{Kind: KindCmd, ID: 1, Sess: dispatchSess, Method: m, Params: raw})
		return rec, reply
	}
	if rec, reply := run(capAll&^CapSteer, MethodPrompt, PromptParams{Text: "hi"}); reply == nil || reply.Error.Code != CodeForbidden || !strings.Contains(reply.Error.Message, "cannot tell whether a talkoot drives") || rec.called != "" {
		t.Fatalf("prompt without steer on a carrier with no answer: %+v, called %q", reply, rec.called)
	}
	if rec, reply := run(capAll, MethodPrompt, PromptParams{Text: "hi"}); reply != nil || rec.called != "Prompt" {
		t.Fatalf("prompt with steer on a carrier with no answer: %+v, called %q", reply, rec.called)
	}
	if _, reply := run(CapRead, MethodContextGet, nil); reply != nil && reply.Error.Code == CodeForbidden {
		t.Fatalf("a read on a carrier with no answer: %+v", reply.Error)
	}
	// A write that names no session is not the rule's to refuse.
	if _, reply := run(capAll&^CapSteer, MethodModelFavorite, FavoriteParams{}); reply != nil && reply.Error.Code == CodeForbidden {
		t.Fatalf("a workspace write on a carrier with no answer: %+v", reply.Error)
	}
}

// The census reads the fields json puts at the top of a params object: through
// a pointer and an embedded struct, and not into a named nested struct. A field
// with no tag keys by its name, and a skipped field keys by nothing.
func TestParamsFieldsFollowsJSONsTopLevel(t *testing.T) {
	type inner struct {
		Session string `json:"session"`
	}
	type params struct {
		inner
		Other   inner  `json:"other"`
		ID      string `json:"id"`
		Session string
		Skipped string `json:"-"`
	}
	var names []string
	for _, f := range paramsFields(reflect.TypeOf(&params{})) {
		names = append(names, jsonName(f))
	}
	if got := strings.Join(names, ","); got != "session,other,id,session," {
		t.Fatalf("paramsFields read %q, want session,other,id,session,", got)
	}
}
