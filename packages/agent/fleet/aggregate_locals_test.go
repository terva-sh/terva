package fleet

import (
	"context"
	"errors"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// creatableSvc is a workspace that can answer CreateSession, so a test can
// see which source a session-less method landed on.
type creatableSvc struct {
	*fakeSvc
	created chan string // the title of each session created here
}

func newCreatableSvc(sessions ...ctrlproto.SessionInfo) *creatableSvc {
	return &creatableSvc{fakeSvc: newFakeSvc(sessions...), created: make(chan string, 4)}
}

func (c *creatableSvc) CreateSession(_ context.Context, opts ctrlproto.CreateOpts) (ctrlproto.SessionInfo, error) {
	c.created <- opts.Title
	return ctrlproto.SessionInfo{ID: "new1", Title: opts.Title}, nil
}

// TestACommandOnASecondLocalSourceIsRouted is step 1 of workspaces-as-sources:
// the refusal in RouteCommand is about members, not about "anything that is
// not the default". A second in-process workspace has no socket on the path,
// so a command to it cannot land on another daemon, and it routes. The member
// in the same fleet is still refused, which is the property the refusal exists
// for and the one this must not loosen.
func TestACommandOnASecondLocalSourceIsRouted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agg, local, _ := startAggregate(t, ctx)
	second := newFakeSvc(ctrlproto.SessionInfo{ID: "p1", Model: "proj-model"})
	if err := agg.AddLocal(Source{Origin: "proj", Svc: second}); err != nil {
		t.Fatalf("AddLocal: %v", err)
	}

	src, id, err := agg.RouteCommand("proj/p1")
	if err != nil {
		t.Fatalf("a command on a second local source was refused: %v", err)
	}
	if id != "p1" || src.Svc != ctrlproto.WorkspaceService(second) {
		t.Errorf("routed to (%q, %v), want (p1, the second local workspace)", id, src.Svc)
	}

	// The default still routes, and the member is still refused.
	if src, _, err := agg.RouteCommand(LocalOrigin + "/l1"); err != nil || src.Svc != ctrlproto.WorkspaceService(local) {
		t.Errorf("the default no longer routes: %v", err)
	}
	if _, _, err := agg.RouteCommand("neot/m1"); !errors.Is(err, ErrRemoteNotRouted) {
		t.Errorf("a member command returned %v, want ErrRemoteNotRouted: adding a local loosened the member refusal", err)
	}

	// And the board lists all three, each under its origin.
	list, err := agg.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range list {
		got[s.ID] = s.Origin
	}
	for id, origin := range map[string]string{"local/l1": "local", "proj/p1": "proj", "neot/m1": "neot"} {
		if got[id] != origin {
			t.Errorf("session %s listed with origin %q, want %q (all: %v)", id, got[id], origin, got)
		}
	}
}

// TestSessionLessMethodsGoToTheDefaultSource pins which local a session-less
// method lands on once there is more than one. The default is the directory
// the daemon started in, and it stays the answer until CreateOpts can name a
// target (step 3). It also pins the result's shape: the created session comes
// back federated, the way the board lists it, so a client that creates and
// then addresses what it was handed lands on the same session.
func TestSessionLessMethodsGoToTheDefaultSource(t *testing.T) {
	def := newCreatableSvc()
	other := newCreatableSvc()
	agg, err := NewAggregate(nil, Source{Origin: LocalOrigin, Svc: def})
	if err != nil {
		t.Fatal(err)
	}
	if err := agg.AddLocal(Source{Origin: "proj", Svc: other}); err != nil {
		t.Fatal(err)
	}
	hs, err := NewHubService(agg)
	if err != nil {
		t.Fatal(err)
	}

	info, err := hs.CreateSession(context.Background(), ctrlproto.CreateOpts{Title: "where"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	select {
	case title := <-def.created:
		if title != "where" {
			t.Errorf("the default created %q, want where", title)
		}
	default:
		t.Fatal("CreateSession did not reach the default source")
	}
	select {
	case <-other.created:
		t.Fatal("CreateSession ALSO reached the second local source")
	default:
	}
	if info.ID != "local/new1" || info.Origin != LocalOrigin {
		t.Errorf("created session came back as (%q, origin %q), want (local/new1, local): "+
			"a bare id here is the special case Sessions() forbids", info.ID, info.Origin)
	}
}

// TestAddLocalRefusesACollidingOrigin: a directory can never shadow the default
// or a machine, and the default cannot be removed.
func TestAddLocalRefusesACollidingOrigin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agg, _, _ := startAggregate(t, ctx)
	for _, origin := range []string{LocalOrigin, "neot"} {
		if err := agg.AddLocal(Source{Origin: origin, Svc: newFakeSvc()}); !errors.Is(err, ErrOriginTaken) {
			t.Errorf("AddLocal(%q) returned %v, want ErrOriginTaken", origin, err)
		}
	}
	if err := agg.AddLocal(Source{Origin: "a/b", Svc: newFakeSvc()}); err == nil {
		t.Error("AddLocal accepted an origin containing the federation separator")
	}
	if err := agg.AddLocal(Source{Origin: "proj"}); err == nil {
		t.Error("AddLocal accepted a source with no workspace")
	}

	if err := agg.AddLocal(Source{Origin: "proj", Svc: newFakeSvc()}); err != nil {
		t.Fatal(err)
	}
	if agg.RemoveLocal(LocalOrigin) {
		t.Error("RemoveLocal removed the default")
	}
	if !agg.RemoveLocal("proj") {
		t.Error("RemoveLocal did not remove proj")
	}
	if _, ok := agg.sourceFor("proj"); ok {
		t.Error("proj is still a source after removal")
	}
	if def, ok := agg.Default(); !ok || def.Origin != LocalOrigin {
		t.Errorf("Default is (%v, %v) after removals, want local", def.Origin, ok)
	}
}

// TestAHubWithNoLocalWorkspaceRefusesSessionLessCalls: a dedicated hub host
// has members and no directory of its own. A session-less call there must fail
// with a reason, not pick a member to answer for the hub.
func TestAHubWithNoLocalWorkspaceRefusesSessionLessCalls(t *testing.T) {
	agg, err := NewAggregate(nil, Source{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := agg.Default(); ok {
		t.Fatal("a hub built with no local workspace reports a default")
	}
	hs, err := NewHubService(agg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hs.CreateSession(context.Background(), ctrlproto.CreateOpts{}); !errors.Is(err, ErrUnknownOrigin) {
		t.Errorf("CreateSession on a hub with no local returned %v, want ErrUnknownOrigin", err)
	}
	if _, _, err := agg.Route("bare"); !errors.Is(err, ErrUnknownOrigin) {
		t.Errorf("a bare id on a hub with no local returned %v, want ErrUnknownOrigin", err)
	}
}
