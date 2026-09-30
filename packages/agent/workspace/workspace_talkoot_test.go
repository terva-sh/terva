package workspace

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/internal/coretest"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/worker"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/session"
	"terva.sh/terva/packages/testsupport"
)

// A roster built from what terva ships must validate against the live
// library and registry, and a name that does not exist must fail by name.
func TestTalkootEnvResolvesTheShippedNames(t *testing.T) {
	const roster = `---
name: crew
home: /src/project
budget_usd_per_day: 10
members:
  - id: helm
    role: coordinator
    persona: mieli
    tier: strong
  - id: jev
    role: specialist
    driver: claude
    workspace: worktree
  - id: tess
    role: specialist
    driver: terva
    workspace: worktree
    tier: medium
---
`
	r, err := talkoot.Parse([]byte(roster), "crew.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := talkoot.Validate(r, talkootEnv()); err != nil {
		t.Fatalf("a roster of shipped names must validate: %v", err)
	}

	r.Members[0].Persona = "no-such-persona"
	r.Members[1].Driver = "no-such-driver"
	err = talkoot.Validate(r, talkootEnv())
	var p *talkoot.Problems
	if !errors.As(err, &p) || len(p.List) != 2 {
		t.Fatalf("want two problems, got %v", err)
	}
	for _, name := range []string{`"no-such-persona"`, `"no-such-driver"`} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the problem must name %s: %v", name, err)
		}
	}
}

// Every shipped backend reports cost today. A new backend that does not must
// say so, and then a roster needs turns_per_day for it.
func TestShippedBackendsReportCost(t *testing.T) {
	for _, name := range worker.Names() {
		b, err := worker.Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		if !b.ReportsCost {
			t.Errorf("backend %s does not report cost; if that is true now, a roster needs turns_per_day for it and this test should list it", name)
		}
	}
}

// promptRecorder is a model that answers every turn with "ok" and records the
// user text of each request.
type promptRecorder struct {
	mu   sync.Mutex
	seen []string
}

func (c *promptRecorder) Name() string { return "prompt-recorder" }

func (c *promptRecorder) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	var sb strings.Builder
	for _, m := range req.Messages {
		if m.Role != provider.RoleUser {
			continue
		}
		for _, b := range m.Content {
			if tb, ok := b.(provider.TextBlock); ok {
				sb.WriteString(tb.Text + "\n")
			}
		}
	}
	c.mu.Lock()
	c.seen = append(c.seen, sb.String())
	c.mu.Unlock()
	out := make(chan provider.Event, 1)
	out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
		Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "ok"}},
	}}
	close(out)
	return out, nil
}

func (c *promptRecorder) await(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		for _, s := range c.seen {
			if strings.Contains(s, want) {
				c.mu.Unlock()
				return
			}
		}
		c.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the model never received %q", want)
}

func bindOnly(member, id string) memberBinding {
	return func(_ string, m talkoot.Member) (string, error) {
		if m.ID == member {
			return id, nil
		}
		return "", errors.New("nothing is bound")
	}
}

// testRun wraps a router the test built, the way startTalkoot wraps one.
func testRun(id string, rt *talkoot.Router, r talkoot.Roster) *talkootRun {
	run := &talkootRun{id: id, router: rt, seats: map[string]string{}, emit: func(talkootEvent) {}}
	run.roster.Store(&r)
	return run
}

func sessionResolver(s *wsSession) func(string) (*wsSession, error) {
	return func(id string) (*wsSession, error) {
		if id != s.id {
			return nil, errors.New("no such session")
		}
		return s, nil
	}
}

// A post from a person reaches an idle native member as a new turn, through
// the router and the session's own queue.
func TestARoutedPostStartsATurnOnAnIdleNativeMember(t *testing.T) {
	model := &promptRecorder{}
	s := newTurnTestSession(t, model)
	r, err := talkoot.Parse([]byte("---\nname: t\nhome: /x\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n---\n"), "t.md")
	if err != nil {
		t.Fatal(err)
	}
	r.ID = "crew"
	drivers := talkoot.Drivers{
		Native: talkootNativeDriver{sessionOf: bindOnly("helm", s.id), resolve: sessionResolver(s),
			read: func(string, string, talkoot.Receipt) {}},
		Worker: talkootWorkerDriver{deliver: func(talkoot.Member, string, *talkoot.Receipt) error { return nil }},
	}
	dir := testsupport.TempDir(t)
	if err := talkoot.CreateRoom(dir); err != nil {
		t.Fatal(err)
	}
	rt, err := talkoot.NewRouter(r, talkoot.OpenRoom(dir), drivers, talkoot.DefaultLimits(), nil)
	if err != nil {
		t.Fatal(err)
	}
	e, err := rt.Post("sothr", nil, "Plan the lake schema.", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	model.await(t, "[talkoot crew] message from sothr (the person you work for), hop 0, envelope "+e.ID+"\n> Plan the lake schema.")
}

// A busy native member takes the envelope at its next safe boundary. It does
// not start a second turn, and it does not interrupt the running tool.
func TestABusyNativeMemberQueuesTheEnvelope(t *testing.T) {
	tool := gatedStepTool{entered: make(chan struct{}, 1), release: make(chan struct{})}
	s := newTurnTestSession(t, &twoStepClient{})
	s.agent.SetTools(core.Registry{"step": tool})
	queues := collectQueueEvents(t, s)
	if err := s.prompt("run the tool", nil, core.UserMessageExtras{}); err != nil {
		t.Fatal(err)
	}
	<-tool.entered

	d := talkootNativeDriver{sessionOf: bindOnly("atlas", s.id), resolve: sessionResolver(s)}
	if err := d.Deliver("crew", talkoot.Member{ID: "atlas"}, "[talkoot crew] message from helm"); err != nil {
		t.Fatal(err)
	}
	if q, ok := queues.await(func(q []string) bool { return len(q) == 1 && q[0] == "[talkoot crew] message from helm" }); !ok {
		t.Fatalf("the envelope must wait in the running turn's queue; saw %v", q)
	}
	close(tool.release)
}

func TestDriversNameAMemberWithNothingBound(t *testing.T) {
	native := talkootNativeDriver{sessionOf: bindOnly("", ""), resolve: func(string) (*wsSession, error) { return nil, nil }}
	if err := native.Deliver("crew", talkoot.Member{ID: "helm"}, "x"); err == nil || !strings.Contains(err.Error(), "member helm: nothing is bound") {
		t.Errorf("native: %v", err)
	}
	w := talkootWorkerDriver{deliver: func(talkoot.Member, string, *talkoot.Receipt) error { return errors.New("external workers are off") }}
	if err := w.Deliver("crew", talkoot.Member{ID: "yelp"}, "x"); err == nil || !strings.Contains(err.Error(), "member yelp: external workers are off") {
		t.Errorf("worker: %v", err)
	}
}

type recordDriver struct {
	mu  sync.Mutex
	got []string
}

func (d *recordDriver) Deliver(_ string, m talkoot.Member, text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.got = append(d.got, m.ID+": "+text)
	return nil
}

// 🚨 The Talkoot tools appear only in a session that holds a seat, so every
// other session keeps its tool footprint (decision 0009). The seat speaks as
// its own member: the model never names the sender. The checks read the
// registry each session published, so they cover the rebuild too.
func TestTalkootToolsOnlyInASeatedSession(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	w := &Workspace{ctx: context.Background(), diag: func(string) {}}
	s1, s2 := newSeatSession(t, w, "s1"), newSeatSession(t, w, "s2")
	w.sessions = map[string]*wsSession{"s1": s1, "s2": s2}
	names := []string{"talkoot_send", "talkoot_handoff", "talkoot_roster"}
	has := func(s *wsSession, name string) bool {
		_, ok := s.agent.LookupTool(name)
		return ok
	}
	for _, n := range names {
		if has(s1, n) {
			t.Fatalf("%s is there before the session holds a seat", n)
		}
	}

	r, err := talkoot.Parse([]byte("---\nname: t\nhome: /x\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n  - id: jev\n    role: specialist\n---\n"), "t.md")
	if err != nil {
		t.Fatal(err)
	}
	r.ID = "crew"
	dir := testsupport.TempDir(t)
	if err := talkoot.CreateRoom(dir); err != nil {
		t.Fatal(err)
	}
	native := &recordDriver{}
	rt, err := talkoot.NewRouter(r, talkoot.OpenRoom(dir), talkoot.Drivers{Native: native, Worker: native}, talkoot.DefaultLimits(), nil)
	if err != nil {
		t.Fatal(err)
	}

	run := testRun("crew", rt, r)
	w.seatTalkoot("s1", run, "jev")
	for _, n := range names {
		if !has(s1, n) {
			t.Fatalf("%s is missing from the seated session", n)
		}
		if has(s2, n) {
			t.Fatalf("%s leaked into a session with no seat", n)
		}
	}

	// A member replies only inside work a person started.
	if _, err := rt.Post("sothr", []string{"jev"}, "Draft the schema.", nil, ""); err != nil {
		t.Fatal(err)
	}
	send, _ := s1.agent.LookupTool("talkoot_send")
	if _, err := send.Execute(context.Background(), []byte(`{"to":["helm"],"kind":"message","body":"The schema is ready."}`), nil); err != nil {
		t.Fatal(err)
	}
	native.mu.Lock()
	got := strings.Join(native.got, "\n")
	native.mu.Unlock()
	if !strings.Contains(got, "helm: ") || !strings.Contains(got, "message from jev") {
		t.Errorf("want helm to get a message from jev, got %q", got)
	}
	roster, _ := s1.agent.LookupTool("talkoot_roster")
	res, err := roster.Execute(context.Background(), []byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if text := res.Content[0].(provider.TextBlock).Text; !strings.Contains(text, "- jev (you)") || !strings.Contains(text, "- helm") {
		t.Errorf("the roster lost a member or the seat's own mark: %q", text)
	}

	// 🚨 An instance a turn still holds stops when the seat changes hands.
	w.seatTalkoot("s1", run, "helm")
	if _, err := send.Execute(context.Background(), []byte(`{"to":["jev"],"kind":"message","body":"Still jev."}`), nil); err == nil || !strings.Contains(err.Error(), "no longer holds") {
		t.Errorf("a tool from the old seat still sent: %v", err)
	}
	if _, err := roster.Execute(context.Background(), []byte(`{}`), nil); err == nil || !strings.Contains(err.Error(), "no longer holds") {
		t.Errorf("a roster from the old seat still answered: %v", err)
	}

	w.unseatTalkoot("s1")
	for _, n := range names {
		if has(s1, n) {
			t.Fatalf("%s outlived the seat", n)
		}
	}
}

// newSeatSession builds a session with a live agent, so rebuildTools publishes
// a registry. The key is a placeholder and no request is ever made: Resolve
// only needs a provider it can name.
func newSeatSession(t *testing.T, w *Workspace, id string) *wsSession {
	t.Helper()
	tmp := testsupport.TempDir(t)
	sess, err := session.NewSession(tmp, tmp, "openai-compatible", "fake-model", "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	s := &wsSession{
		id: id, ws: w, hub: newWSHub(), sess: sess, cwd: tmp,
		askReq: map[string]ctrlproto.AskRequest{},
		args:   build.Args{Provider: "openai-compatible", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "fake-model", CWD: tmp},
	}
	r, err := build.Resolve(s.args, true)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	s.agent = coretest.NewAgent(nil, "fake-model", "", r.ToolRegistry)
	return s
}

// gateDriver blocks a delivery to one member until the test releases it.
type gateDriver struct {
	member  string
	entered chan struct{}
	release chan struct{}
}

func (d *gateDriver) Deliver(_ string, m talkoot.Member, _ string) error {
	if m.ID == d.member {
		d.entered <- struct{}{}
		<-d.release
	}
	return nil
}

// 🚨 An unseat waits for a send in flight, and no send starts after it
// returns. A check that let go of its lock before the router call would leave
// a gap for one more send as the old member.
func TestUnseatWaitsForASendInFlight(t *testing.T) {
	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
	r, err := talkoot.Parse([]byte("---\nname: t\nhome: /x\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n  - id: jev\n    role: specialist\n---\n"), "t.md")
	if err != nil {
		t.Fatal(err)
	}
	r.ID = "crew"
	dir := testsupport.TempDir(t)
	if err := talkoot.CreateRoom(dir); err != nil {
		t.Fatal(err)
	}
	gate := &gateDriver{member: "helm", entered: make(chan struct{}, 1), release: make(chan struct{})}
	rt, err := talkoot.NewRouter(r, talkoot.OpenRoom(dir), talkoot.Drivers{Native: gate, Worker: gate}, talkoot.DefaultLimits(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Post("sothr", []string{"jev"}, "Draft the schema.", nil, ""); err != nil {
		t.Fatal(err)
	}
	run := testRun("crew", rt, r)
	w.seatTalkoot("s1", run, "jev")
	seat, _ := w.talkootSeatOf("s1")
	out := talkoot.Outgoing{To: []string{"helm"}, Kind: talkoot.KindMessage, Body: "The schema is ready."}

	sent := make(chan error, 1)
	go func() {
		_, err := seat.Send(out)
		sent <- err
	}()
	<-gate.entered
	unseated := make(chan struct{})
	go func() {
		w.unseatTalkoot("s1")
		close(unseated)
	}()
	select {
	case <-unseated:
		t.Fatal("the unseat returned while a send was still in flight")
	case <-time.After(50 * time.Millisecond):
	}
	close(gate.release)
	if err := <-sent; err != nil {
		t.Fatalf("the send in flight should finish: %v", err)
	}
	<-unseated
	if _, err := seat.Send(out); !errors.Is(err, errSeatRevoked) {
		t.Errorf("a send after the unseat went through: %v", err)
	}
}
