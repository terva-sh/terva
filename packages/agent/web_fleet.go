//go:build terva_web

package agent

import (
	"context"
	"fmt"
	"os"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/fleet"
)

// startFleetHub turns this `terva web` into a fleet hub.
//
// It opens a SECOND listener for members to check in on, separate from the
// browser's, and returns the aggregating service that web.Serve should use in
// place of the local workspace. Browsers and members carry different
// credentials and want different exposure, and one listener cannot hold both
// postures.
//
// The caller only reaches here when --fleet-addr is set. With the flag absent,
// `terva web` serves its own workspace exactly as before, which is the
// no-regression rule the hub shape was chosen for in the first place.
//
// The returned stop function closes the hub. It does not close ws, which the
// caller owns.
func fleetServiceFor(ctx context.Context, args build.Args, ws ctrlproto.WorkspaceService, version string) (ctrlproto.WorkspaceService, func(), error) {
	// No --fleet-addr, no fleet. The workspace goes to web.Serve untouched and
	// no second listener opens. This early return is the no-regression rule in
	// one line, and TestWebModeNoFleetAddrHandsBackTheWorkspaceUntouched pins it.
	if args.FleetAddr == "" {
		return ws, func() {}, nil
	}

	token, err := readFleetToken(args.FleetTokenFile)
	if err != nil {
		return nil, nil, err
	}

	// Bind safety before anything else, so an unsafe address never reaches a
	// socket. fleet.Listen refuses past loopback and unix while member identity
	// is a shared bearer, and its message already explains the constraint and
	// the tunnel. Add only what it cannot know: the flag the person typed, and
	// one command they can paste.
	ln, err := fleet.Listen(args.FleetAddr)
	if err != nil {
		// -L and not -R. The member needs to REACH the hub's loopback, so it
		// listens locally and forwards to a port resolved on the hub. -R is the
		// other direction: it would open a port on the hub forwarding back to the
		// member, which is useless here and collides with the hub's own listener.
		return nil, nil, fmt.Errorf("--fleet-addr %s: %w\nFor example, from the member: ssh -N -L 8081:127.0.0.1:8081 HUBHOST, then --hub ws://127.0.0.1:8081", args.FleetAddr, err)
	}

	hub, err := fleet.NewHub(fleet.HubOptions{
		Token: token,
		Hello: ctrlproto.ServerHello("terva-hub", version),
		OnMemberUp: func(origin string, server ctrlproto.Hello) {
			fmt.Fprintf(os.Stderr, "terva web: member %q checked in (%s %s)\n", origin, server.Agent, server.Version)
		},
		OnMemberDown: func(origin string, err error) {
			fmt.Fprintf(os.Stderr, "terva web: member %q went away: %v\n", origin, err)
		},
	})
	if err != nil {
		ln.Close()
		return nil, nil, err
	}

	agg, err := fleet.NewAggregate(hub, ws, fleet.LocalOrigin)
	if err != nil {
		hub.Close()
		ln.Close()
		return nil, nil, err
	}
	// One member being unreachable must not blank the board. A fleet view is
	// least useful at the moment a daemon breaks.
	agg.OnSourceError = func(origin string, err error) {
		fmt.Fprintf(os.Stderr, "terva web: member %q did not answer: %v\n", origin, err)
	}

	svc, err := fleet.NewHubService(agg)
	if err != nil {
		hub.Close()
		ln.Close()
		return nil, nil, err
	}

	go func() {
		if err := hub.Serve(ctx, ln); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "terva web: member endpoint stopped: %v\n", err)
		}
	}()

	fmt.Fprintf(os.Stderr, "terva web: fleet hub accepting members on %s\n", args.FleetAddr)
	return svc, func() { hub.Close() }, nil
}
