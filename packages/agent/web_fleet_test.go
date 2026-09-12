//go:build terva_web

package agent

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/fleet"
	"terva.sh/terva/packages/testsupport"
)

// fleetFakeWS stands in for a real Workspace. It answers Sessions and embeds a
// nil interface for everything else, so a call nobody meant to make panics with
// the method name instead of returning a zero value.
type fleetFakeWS struct {
	ctrlproto.WorkspaceService
	sessions []ctrlproto.SessionInfo
}

func (f *fleetFakeWS) Sessions(context.Context) ([]ctrlproto.SessionInfo, error) {
	return append([]ctrlproto.SessionInfo(nil), f.sessions...), nil
}

func writeFleetToken(t *testing.T) string {
	t.Helper()
	path := filepath.Join(testsupport.TempDir(t), "fleet-token")
	if err := os.WriteFile(path, []byte("fleet-test-bearer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// freeLoopbackAddr returns a loopback address nothing is listening on.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// TestWebModeNoFleetAddrHandsBackTheWorkspaceUntouched is the no-regression
// rule.
//
// The TestWebMode prefix is load-bearing, not decoration: CI runs the tagged
// suite with -run '^TestWebMode', so a tagged test named anything else is
// skipped and the step still exits 0. packages/testsupport guards that.
//
// `terva web` with no --fleet-addr must be the daemon it has always been. The
// assertion is identity, not equivalence: web.Serve has to receive the very
// workspace it received before, not a wrapper that behaves like one. A wrapper
// would be a new failure surface in the single-machine path, which has no fleet
// to justify it.
func TestWebModeNoFleetAddrHandsBackTheWorkspaceUntouched(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ws := &fleetFakeWS{sessions: []ctrlproto.SessionInfo{{ID: "l1"}}}

	// A token file is deliberately configured here. Even with fleet credentials
	// lying around, no --fleet-addr means no fleet.
	args := build.Args{FleetTokenFile: writeFleetToken(t)}

	svc, stop, err := fleetServiceFor(ctx, args, ws, "test")
	if err != nil {
		t.Fatalf("fleetServiceFor with no --fleet-addr: %v", err)
	}
	if stop == nil {
		t.Fatal("stop is nil, so the caller's defer would panic")
	}
	defer stop()

	if svc != ctrlproto.WorkspaceService(ws) {
		t.Errorf("web.Serve would receive %T instead of the workspace itself. "+
			"With no fleet configured the single-machine path must not gain a wrapper", svc)
	}

	// The stop function must be safe to call when nothing was started.
	stop()
}

// TestWebModeUnsafeFleetAddrIsRefusedWithAWayOut covers the bind-safety
// criterion.
//
// fleet.Listen already refuses anything past loopback and unix while member
// identity is a shared bearer. What this pins is that the refusal reaching a
// person names the flag they typed and the way out, because a member on another
// machine can only reach a loopback hub through a tunnel.
func TestWebModeUnsafeFleetAddrIsRefusedWithAWayOut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tokenFile := writeFleetToken(t)
	ws := &fleetFakeWS{}

	for _, addr := range []string{"0.0.0.0:8081", ":8081", "100.73.27.67:8081", "[::]:8081"} {
		t.Run(addr, func(t *testing.T) {
			svc, stop, err := fleetServiceFor(ctx, build.Args{
				FleetAddr:      addr,
				FleetTokenFile: tokenFile,
			}, ws, "test")
			if err == nil {
				if stop != nil {
					stop()
				}
				t.Fatalf("--fleet-addr %s was accepted: the member endpoint is open to the network "+
					"while every member shares one bearer token", addr)
			}
			if svc != nil {
				t.Error("a refused bind still handed back a service")
			}

			msg := err.Error()
			if !strings.Contains(msg, "--fleet-addr") {
				t.Errorf("the refusal does not name the flag the person typed: %v", err)
			}
			// The constraint comes from CheckMemberBindSafety, the pasteable
			// command from the wrapper. Both have to survive, because the first
			// says why and the second says what to do instead.
			if !strings.Contains(msg, "loopback") || !strings.Contains(msg, "unix:") {
				t.Errorf("the refusal does not name the loopback and unix constraint: %v", err)
			}
			if !strings.Contains(msg, "tunnel") {
				t.Errorf("the refusal does not say a member reaches a loopback hub through a tunnel: %v", err)
			}
			// -L, because the member forwards its own loopback to the hub's. An
			// -R here would tunnel the opposite way and shipped wrong once.
			if !strings.Contains(msg, "ssh -N -L") {
				t.Errorf("the refusal does not give a command to paste: %v", err)
			}
		})
	}
}

// TestWebModeMemberChecksInThroughTheFleetEndpoint is the end-to-end criterion: the
// flag opens a listener, a real member dials it, and the service handed to
// web.Serve then shows that member's sessions.
//
// It drives fleetServiceFor, the same function runWebMode calls, so the wiring
// under test is the wiring that ships.
func TestWebModeMemberChecksInThroughTheFleetEndpoint(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := freeLoopbackAddr(t)
	tokenFile := writeFleetToken(t)
	hubWS := &fleetFakeWS{sessions: []ctrlproto.SessionInfo{{ID: "l1"}}}

	svc, stop, err := fleetServiceFor(ctx, build.Args{
		FleetAddr:      addr,
		FleetTokenFile: tokenFile,
	}, hubWS, "test")
	if err != nil {
		t.Fatalf("fleetServiceFor: %v", err)
	}
	defer stop()

	memberWS := &fleetFakeWS{sessions: []ctrlproto.SessionInfo{{ID: "m1", Model: "member-model"}}}
	m, err := fleet.NewMember(fleet.MemberOptions{
		HubURL:  "ws://" + addr,
		Token:   "fleet-test-bearer",
		Origin:  "neot",
		Service: memberWS,
		Hello:   ctrlproto.ServerHello("terva-member", "test"),
		Backoff: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMember: %v", err)
	}
	go func() { _ = m.Run(ctx) }()

	deadline := time.Now().Add(15 * time.Second)
	for {
		got, err := svc.Sessions(ctx)
		if err != nil {
			t.Fatalf("Sessions through the hub service: %v", err)
		}
		if len(got) == 2 {
			byID := map[string]ctrlproto.SessionInfo{}
			for _, s := range got {
				byID[s.ID] = s
			}
			remote, ok := byID["neot/m1"]
			if !ok {
				t.Fatalf("the member checked in but its session is not federated as neot/m1: %+v", got)
			}
			if remote.Origin != "neot" {
				t.Errorf("the member's session carries origin %q, want neot", remote.Origin)
			}
			if remote.Model != "member-model" {
				t.Errorf("the member's own fields did not survive the hop: model %q", remote.Model)
			}
			if _, ok := byID["local/l1"]; !ok {
				t.Errorf("the hub's own session is missing from the union: %+v", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the member never appeared through --fleet-addr %s; sessions: %+v", addr, got)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
