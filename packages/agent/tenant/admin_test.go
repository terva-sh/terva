package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// pipeConn is an in-memory FrameConn: the test writes what a client would send
// and reads what the server answers.
type pipeConn struct {
	in  chan ctrlproto.Frame // client → server
	out chan ctrlproto.Frame // server → client
}

func newPipeConn() *pipeConn {
	return &pipeConn{in: make(chan ctrlproto.Frame, 16), out: make(chan ctrlproto.Frame, 16)}
}

func (p *pipeConn) ReadFrame(ctx context.Context) (ctrlproto.Frame, error) {
	select {
	case <-ctx.Done():
		return ctrlproto.Frame{}, ctx.Err()
	case f, ok := <-p.in:
		if !ok {
			return ctrlproto.Frame{}, errors.New("closed")
		}
		return f, nil
	}
}

func (p *pipeConn) WriteFrame(ctx context.Context, f ctrlproto.Frame) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case p.out <- f:
		return nil
	}
}

func (p *pipeConn) Close() error { return nil }

// next reads the server's next frame, failing rather than hanging.
func (p *pipeConn) next(t *testing.T) ctrlproto.Frame {
	t.Helper()
	select {
	case f := <-p.out:
		return f
	case <-time.After(5 * time.Second):
		t.Fatal("the supervisor answered nothing")
		return ctrlproto.Frame{}
	}
}

// dialAdmin starts ServeAdmin against ctl and completes the handshake, returning
// the pipe and the server's hello.
func dialAdmin(t *testing.T, ctl ctrlproto.TenantsController, groups []ctrlproto.Group, mask ctrlproto.Capability) (*pipeConn, ctrlproto.Hello) {
	t.Helper()
	conn := newPipeConn()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = ServeAdmin(ctx, conn, ctl, AdminHello("terva serve", "test"), mask) }()

	conn.in <- ctrlproto.HelloFrame(ctrlproto.Hello{
		Role: ctrlproto.RoleClient, Protocol: ctrlproto.Protocol, Groups: groups,
	})
	f := conn.next(t)
	if f.Kind != ctrlproto.KindHello || f.Hello == nil {
		t.Fatalf("first frame back was %q, not a hello", f.Kind)
	}
	return conn, *f.Hello
}

// call sends one command and returns the response frame.
func (p *pipeConn) call(t *testing.T, id uint64, m ctrlproto.Method, params any) ctrlproto.Frame {
	t.Helper()
	f := ctrlproto.Frame{Kind: ctrlproto.KindCmd, ID: id, Method: m}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		f.Params = b
	}
	p.in <- f
	return p.next(t)
}

const everyGroup = ctrlproto.CapRead | ctrlproto.CapWrite | ctrlproto.CapSpend

// 🚨 The boundary from the supervisor's side: its connection offers the tenants
// group and NOTHING else. There is no workspace behind it, so there is no
// session, transcript or model call reachable from an admin connection either —
// the isolation runs in both directions.
func TestTheSupervisorsConnectionOffersOnlyTheTenantsGroup(t *testing.T) {
	skipOnWindows(t)
	p, _, _ := newTestPanel(t, isolating{})

	_, hello := dialAdmin(t, p, []ctrlproto.Group{
		ctrlproto.GroupTenants, ctrlproto.GroupConversation, ctrlproto.GroupSession,
		ctrlproto.GroupControl, ctrlproto.GroupAuth, ctrlproto.GroupSecrets,
	}, everyGroup)

	if len(hello.Groups) != 1 || hello.Groups[0] != ctrlproto.GroupTenants {
		t.Fatalf("the supervisor advertised %v", hello.Groups)
	}
}

// A client that asks for a workspace verb on this connection is told it is not
// served here — the same answer, and the same shape, a tenant gets for a verb
// the proxy will not carry.
func TestAWorkspaceVerbIsNotServedOnTheSupervisorsConnection(t *testing.T) {
	skipOnWindows(t)
	p, _, _ := newTestPanel(t, isolating{})
	conn, _ := dialAdmin(t, p, []ctrlproto.Group{ctrlproto.GroupTenants}, everyGroup)

	for _, m := range []ctrlproto.Method{
		ctrlproto.MethodPrompt, ctrlproto.MethodSessionsList, ctrlproto.MethodModelsList,
		ctrlproto.MethodAuthLoginStart, ctrlproto.MethodSecretsStatus,
		// subscribe would otherwise leave a client waiting forever for a
		// snapshot from an event pump that does not exist here.
		ctrlproto.MethodSubscribe,
	} {
		resp := conn.call(t, 1, m, nil)
		if resp.Error == nil {
			t.Errorf("%s was answered on the supervisor's connection", m)
			continue
		}
		if resp.Error.Code != ctrlproto.CodeUnsupported {
			t.Errorf("%s refused with %q, want unsupported", m, resp.Error.Code)
		}
	}
}

// The verbs work end to end over the wire, which is the part a producer test
// cannot show: params bind, results marshal, errors carry their code.
func TestTheTenantsVerbsWorkOverTheWire(t *testing.T) {
	skipOnWindows(t)
	panel, store, sup := newTestPanel(t, isolating{})
	rec := enrolIn(t, store, "sub-ada", "Ada")
	if _, err := sup.Start(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	conn, _ := dialAdmin(t, panel, []ctrlproto.Group{ctrlproto.GroupTenants}, everyGroup)

	resp := conn.call(t, 1, ctrlproto.MethodTenantsList, nil)
	if resp.Error != nil {
		t.Fatalf("tenants.list: %v", resp.Error)
	}
	var list ctrlproto.TenantsListResult
	if err := json.Unmarshal(resp.Result, &list); err != nil {
		t.Fatalf("decode tenants.list: %v", err)
	}
	if len(list.Tenants) != 1 || !list.Tenants[0].Running {
		t.Fatalf("listing over the wire = %+v", list.Tenants)
	}

	if resp := conn.call(t, 2, ctrlproto.MethodTenantsSuspend, ctrlproto.TenantRef{ID: rec.ID}); resp.Error != nil {
		t.Fatalf("tenants.suspend: %v", resp.Error)
	}
	if got := listOne(t, panel, rec.ID); !got.Suspended {
		t.Error("a suspend over the wire did not reach the registry")
	}

	if resp := conn.call(t, 3, ctrlproto.MethodTenantsResume, ctrlproto.TenantRef{ID: rec.ID}); resp.Error != nil {
		t.Fatalf("tenants.resume: %v", resp.Error)
	}
	if got := listOne(t, panel, rec.ID); got.Suspended {
		t.Error("a resume over the wire did not reach the registry")
	}
}

// A wire error keeps its code rather than collapsing to `internal`, so a client
// can tell "you named nobody" from "the supervisor broke".
func TestAWireErrorKeepsItsCode(t *testing.T) {
	skipOnWindows(t)
	p, _, _ := newTestPanel(t, isolating{})
	conn, _ := dialAdmin(t, p, []ctrlproto.Group{ctrlproto.GroupTenants}, everyGroup)

	resp := conn.call(t, 1, ctrlproto.MethodTenantsSuspend, ctrlproto.TenantRef{ID: "not-an-id"})
	if resp.Error == nil || resp.Error.Code != ctrlproto.CodeBadRequest {
		t.Fatalf("suspend of a malformed id answered %+v", resp.Error)
	}
}

// The capability mask is a real gate, not decoration: a caller who may read the
// listing must not thereby hold the lever.
func TestAReadOnlyOperatorSeesTheListingAndCannotSuspend(t *testing.T) {
	skipOnWindows(t)
	panel, store, _ := newTestPanel(t, isolating{})
	rec := enrolIn(t, store, "sub-ada", "Ada")
	conn, _ := dialAdmin(t, panel, []ctrlproto.Group{ctrlproto.GroupTenants}, ctrlproto.CapRead)

	if resp := conn.call(t, 1, ctrlproto.MethodTenantsList, nil); resp.Error != nil {
		t.Fatalf("a read-only caller was refused the listing: %v", resp.Error)
	}
	resp := conn.call(t, 2, ctrlproto.MethodTenantsSuspend, ctrlproto.TenantRef{ID: rec.ID})
	if resp.Error == nil || resp.Error.Code != ctrlproto.CodeForbidden {
		t.Fatalf("a read-only caller suspended an environment: %+v", resp.Error)
	}
	if got := listOne(t, panel, rec.ID); got.Suspended {
		t.Fatal("the refusal did not prevent the suspension")
	}
}

// A zero mask is the state a carrier that never established who is asking would
// arrive in, and it must deny everything rather than serve the surface.
func TestAConnectionWithNoAuthorityIsRefusedEverything(t *testing.T) {
	skipOnWindows(t)
	p, _, _ := newTestPanel(t, isolating{})
	conn, _ := dialAdmin(t, p, []ctrlproto.Group{ctrlproto.GroupTenants}, 0)

	resp := conn.call(t, 1, ctrlproto.MethodTenantsList, nil)
	if resp.Error == nil || resp.Error.Code != ctrlproto.CodeForbidden {
		t.Fatalf("a connection with no authority read the listing: %+v", resp.Error)
	}
}

// A client that did not negotiate the group must not reach the verbs by naming
// them — the rule ServeConn enforces for every other group.
func TestAClientThatDidNotNegotiateTheGroupIsRefused(t *testing.T) {
	skipOnWindows(t)
	p, _, _ := newTestPanel(t, isolating{})
	// The server hello states what the SERVER serves, exactly as ServeConn's
	// does; the contract is the intersection, and it is held server-side. So
	// the assertion is on the answer, not on the advertisement.
	conn, _ := dialAdmin(t, p, []ctrlproto.Group{ctrlproto.GroupConversation}, everyGroup)

	resp := conn.call(t, 1, ctrlproto.MethodTenantsList, nil)
	if resp.Error == nil || resp.Error.Code != ctrlproto.CodeUnsupported {
		t.Fatalf("tenants.list answered %+v without the group negotiated", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "not negotiated") {
		t.Errorf("the refusal does not say the group was not negotiated: %q", resp.Error.Message)
	}
}

// Every verb in the group must be served by the loop. The switch's default is
// reachable only for one this test has not been told about, and a client meeting
// it would get `unknown method` from the one server that does own the verb.
func TestEveryTenantsVerbIsServed(t *testing.T) {
	skipOnWindows(t)
	panel, store, _ := newTestPanel(t, isolating{})
	rec := enrolIn(t, store, "sub-ada", "Ada")
	conn, _ := dialAdmin(t, panel, []ctrlproto.Group{ctrlproto.GroupTenants}, everyGroup)

	// Every Method constant whose group is tenants, discovered rather than
	// listed, so a verb added to the protocol without a handler fails here.
	for _, m := range tenantsVerbs() {
		resp := conn.call(t, 9, m, ctrlproto.TenantRef{ID: rec.ID})
		if resp.Error != nil && strings.Contains(resp.Error.Message, "unknown method") {
			t.Errorf("%s is in the tenants group and ServeAdmin does not serve it", m)
		}
	}
}

// tenantsVerbs is every Method the protocol assigns to the tenants group.
//
// Listed here rather than parsed out of methods.go because ctrlproto already
// runs that census (TestEveryMethodHasAGroup); what this file needs is the
// crossing check — that each one reaches a handler HERE — and the roster below
// is checked against Group() so it cannot silently fall behind.
func tenantsVerbs() []ctrlproto.Method {
	all := []ctrlproto.Method{
		ctrlproto.MethodTenantsList,
		ctrlproto.MethodTenantsSuspend,
		ctrlproto.MethodTenantsResume,
	}
	for _, m := range all {
		if m.Group() != ctrlproto.GroupTenants {
			panic("tenantsVerbs lists " + string(m) + ", which is not in the tenants group")
		}
	}
	return all
}
