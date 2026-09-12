package fleet

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// The classification the adapter implements, written out here so a test can
// check it against the interface rather than against the adapter. If these
// disagreed with hubservice.go, the adapter would be wrong in exactly the way
// that is hard to see by reading it.

// routedReads reach the member that owns the session.
var routedReads = []string{
	"Context",
	"History",
	"ListResets",
	"Models",
	"Reveal",
	"Subscribe",
	"Surface",
	"Surfaces",
	"ToolDisplays",
	"Usage",
	"UsageSnapshot",
}

// refusedCommands carry a session id and refuse when it names a member.
var refusedCommands = []string{
	"Answer",
	"Approve",
	"Cancel",
	"Clear",
	"Compact",
	"ConsumeReset",
	"DeleteMessage",
	"DeleteSession",
	"EditMessage",
	"ForkSession",
	"GenerateSessionTitle",
	"Node",
	"Prompt",
	"Queue",
	"RenameSession",
	"ResumeSession",
	"ResumeTurn",
	"RetryTurn",
	"SetQueue",
	"SetSessionReasoning",
	"SideChatAsk",
	"SideChatClose",
	"SideChatOpen",
	"SurfaceAction",
	"SwipeMessage",
	"SwipeTurn",
	"SwitchModel",
}

// hubScoped carry no session id. Sessions is here because it takes none; it is
// served from the fan-in rather than from the hub's own workspace, and
// TestSessionsListIsTheUnionWithOriginsAndFederatedIDs covers that.
var hubScoped = []string{
	"AuthProviders",
	"Catalog",
	"CreateSession",
	"ListFiles",
	"Restart",
	"Sessions",
	"SetDefaultModel",
	"SetFavoriteModel",
	"SetModelHidden",
	"Trust",
	"Untrust",
}

// TestEveryWorkspaceMethodIsClassified is the test that a method nobody wrote
// cannot slip past.
//
// The compile-time assertion in hubservice.go proves HubService implements all
// 49 methods. It cannot prove somebody thought about what each one means for a
// fleet. A method added to ctrlproto.WorkspaceService later, implemented here
// by reflex as a passthrough to the hub, would compile and would ship.
//
// This fails instead, and names the method.
func TestEveryWorkspaceMethodIsClassified(t *testing.T) {
	iface := reflect.TypeOf((*ctrlproto.WorkspaceService)(nil)).Elem()

	classified := map[string]string{}
	for _, n := range routedReads {
		classified[n] = "routedReads"
	}
	for _, n := range refusedCommands {
		if prior, dup := classified[n]; dup {
			t.Errorf("%s is in both %s and refusedCommands", n, prior)
		}
		classified[n] = "refusedCommands"
	}
	for _, n := range hubScoped {
		if prior, dup := classified[n]; dup {
			t.Errorf("%s is in both %s and hubScoped", n, prior)
		}
		classified[n] = "hubScoped"
	}

	var unclassified []string
	onInterface := map[string]bool{}
	for i := 0; i < iface.NumMethod(); i++ {
		name := iface.Method(i).Name
		onInterface[name] = true
		if _, ok := classified[name]; !ok {
			unclassified = append(unclassified, name)
		}
	}
	sort.Strings(unclassified)
	if len(unclassified) > 0 {
		t.Errorf("ctrlproto.WorkspaceService has %d method(s) this adapter never classified: %v.\n"+
			"Decide for each one whether it reads from a member, refuses as a command, or is "+
			"hub-scoped, and add it to the matching list. A passthrough added by reflex is how a "+
			"member-addressed call reaches the hub's own workspace.", len(unclassified), unclassified)
	}

	var stale []string
	for name := range classified {
		if !onInterface[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("these names are classified but are not on ctrlproto.WorkspaceService: %v. "+
			"The interface changed and this list did not.", stale)
	}

	if total := len(routedReads) + len(refusedCommands) + len(hubScoped); total != iface.NumMethod() {
		t.Errorf("classified %d methods, the interface has %d", total, iface.NumMethod())
	}
}

// TestNoMemberAddressedCommandTouchesTheLocalWorkspace walks every command with
// a member-addressed id and proves each one refuses.
//
// The local workspace here is a fakeSvc, which implements Sessions and
// Subscribe and embeds a nil interface for the rest. So a command that wrongly
// reached it panics on a nil method rather than quietly succeeding, and the
// recover below reports which one. That is the failure this ticket exists to
// prevent: a command addressed to neot, applied to a session on the hub.
func TestNoMemberAddressedCommandTouchesTheLocalWorkspace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agg, _, _ := startAggregate(t, ctx)
	hs, err := NewHubService(agg)
	if err != nil {
		t.Fatalf("NewHubService: %v", err)
	}

	hv := reflect.ValueOf(hs)
	for _, name := range refusedCommands {
		t.Run(name, func(t *testing.T) {
			m := hv.MethodByName(name)
			if !m.IsValid() {
				t.Fatalf("HubService has no method %s, so this list is stale", name)
			}
			mt := m.Type()
			if mt.IsVariadic() {
				t.Fatalf("%s is variadic and this harness does not build its arguments", name)
			}
			if mt.NumIn() < 2 {
				t.Fatalf("%s takes %d arguments, too few to carry a session id", name, mt.NumIn())
			}

			args := make([]reflect.Value, mt.NumIn())
			args[0] = reflect.ValueOf(ctx)
			if mt.In(1).Kind() != reflect.String {
				t.Fatalf("%s does not take a string where a session id belongs", name)
			}
			args[1] = reflect.ValueOf("neot/m1")
			for i := 2; i < mt.NumIn(); i++ {
				args[i] = reflect.New(mt.In(i)).Elem()
			}

			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s panicked on a member-addressed id: %v.\n"+
						"The local workspace implements almost nothing, so this means the call "+
						"reached it. A command for neot just ran against the hub.", name, r)
				}
			}()

			out := m.Call(args)

			var got error
			for _, o := range out {
				if o.Type() == reflect.TypeOf((*error)(nil)).Elem() && !o.IsNil() {
					got, _ = o.Interface().(error)
				}
			}
			if !errors.Is(got, ErrRemoteNotRouted) {
				t.Errorf("%s returned %v, want ErrRemoteNotRouted", name, got)
			}
		})
	}
}

// TestALocalCommandStillWorks is the positive control for the test above.
// Without it, a HubService that refused every command regardless of origin
// would pass, and the hub could not drive its own sessions.
func TestALocalCommandStillWorks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agg, local, _ := startAggregate(t, ctx)
	hs, err := NewHubService(agg)
	if err != nil {
		t.Fatalf("NewHubService: %v", err)
	}

	// Route rather than call: fakeSvc implements almost nothing, so proving the
	// resolution lands on the local workspace is the assertion available here.
	svc, id, err := hs.command(LocalOrigin + "/l1")
	if err != nil {
		t.Fatalf("a local command was refused: %v", err)
	}
	if id != "l1" {
		t.Errorf("local command resolved to id %q, want l1", id)
	}
	if svc != ctrlproto.WorkspaceService(local) {
		t.Error("a local command did not resolve to the hub's own workspace")
	}
}

// readableSvc is a member that can answer one read, so a routed read has
// something real to come back with.
type readableSvc struct {
	*fakeSvc
	sawHistoryFor chan string
}

func (r *readableSvc) History(_ context.Context, sess string, _, _ int, epoch uint64) (ctrlproto.HistoryResult, error) {
	select {
	case r.sawHistoryFor <- sess:
	default:
	}
	return ctrlproto.HistoryResult{Epoch: epoch, Total: 7}, nil
}

// TestAReadReachesTheMemberWithTheBareID covers the other half of the split.
// Refusing reads would make a member's sessions dead tiles: listed, and then
// unopenable, because the browser cannot fetch the transcript.
//
// It also pins the id handed to the member. The member knows its session as
// m1, never as neot/m1, so a federated id arriving there would find nothing.
func TestAReadReachesTheMemberWithTheBareID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	up := make(chan ctrlproto.Hello, 4)
	hub, hubURL := startHub(t, ctx, HubOptions{
		OnMemberUp: func(_ string, server ctrlproto.Hello) { up <- server },
	})

	remote := &readableSvc{
		fakeSvc:       newFakeSvc(ctrlproto.SessionInfo{ID: "m1"}),
		sawHistoryFor: make(chan string, 4),
	}
	startMember(t, ctx, hubURL, "neot", remote)
	select {
	case <-up:
	case <-time.After(10 * time.Second):
		t.Fatal("the member never checked in")
	}

	agg, err := NewAggregate(hub, newFakeSvc(), LocalOrigin)
	if err != nil {
		t.Fatalf("NewAggregate: %v", err)
	}
	hs, err := NewHubService(agg)
	if err != nil {
		t.Fatalf("NewHubService: %v", err)
	}

	got, err := hs.History(ctx, "neot/m1", 0, 50, 3)
	if err != nil {
		t.Fatalf("History on a member session was refused: %v.\n"+
			"Reads have to route, or the browser lists a member's sessions and then "+
			"cannot open any of them", err)
	}
	if got.Total != 7 {
		t.Errorf("History returned Total %d, want the member's 7", got.Total)
	}

	select {
	case sess := <-remote.sawHistoryFor:
		if sess != "m1" {
			t.Errorf("the member was asked for history on %q, want the bare m1. "+
				"A federated id means nothing on the member that owns the session", sess)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the member never saw the history call")
	}
}

// TestRestartDoesNotReachAMember pins the sharpest of the hub-scoped rules.
//
// Restart carries no session id, so there is no member to address and nothing
// to route by. The danger is not ambiguity, it is blast radius: a hub that
// restarted its fleet would take down every member from a button that says
// nothing about them.
func TestRestartDoesNotReachAMember(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	up := make(chan ctrlproto.Hello, 4)
	hub, hubURL := startHub(t, ctx, HubOptions{
		OnMemberUp: func(_ string, server ctrlproto.Hello) { up <- server },
	})

	remote := &restartSpy{fakeSvc: newFakeSvc(ctrlproto.SessionInfo{ID: "m1"})}
	startMember(t, ctx, hubURL, "neot", remote)
	select {
	case <-up:
	case <-time.After(10 * time.Second):
		t.Fatal("the member never checked in")
	}

	local := &restartSpy{fakeSvc: newFakeSvc(ctrlproto.SessionInfo{ID: "l1"})}
	agg, err := NewAggregate(hub, local, LocalOrigin)
	if err != nil {
		t.Fatalf("NewAggregate: %v", err)
	}
	hs, err := NewHubService(agg)
	if err != nil {
		t.Fatalf("NewHubService: %v", err)
	}

	if err := hs.Restart(ctx); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	if !local.restarted {
		t.Error("Restart did not reach the hub's own workspace, so it did nothing at all")
	}

	// The member is driven over a socket, so give a wrongly forwarded restart
	// time to arrive before concluding it never will.
	time.Sleep(300 * time.Millisecond)
	if remote.restarted {
		t.Error("Restart reached a member. One button on the hub just restarted " +
			"another machine, and the fleet has no way to say so")
	}
}

type restartSpy struct {
	*fakeSvc
	restarted bool
}

func (r *restartSpy) Restart(context.Context) error {
	r.restarted = true
	return nil
}
